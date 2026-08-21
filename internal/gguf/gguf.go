// Package gguf reads a GGUF header without loading weights.
//
// This exists to catch a specific, silent submission failure. adtc-profiler
// derives the model's real parameter count from the GGUF (preferring the
// `general.parameter_count` key, falling back to summing the tensor table) and
// compares it against metadata.json's `model.parameters_estimate` with a ±15%
// two-sided tolerance. A mismatch surfaces in the audit report as
// `model_info.params_match: false` — a fraud-detection signal — even when the
// participant simply quoted the upstream model card.
//
// That is exactly the trap here. Qwen publishes Qwen2.5-1.5B-Instruct as 1.54B
// parameters, but the official GGUF conversion materialises the output
// projection as its own tensor instead of tying it to the input embedding, so
// the tensor table sums to ~1.78B. Claiming "1.5B" would fail the check
// (1.78B > 1.5B × 1.15) despite being what the model card says.
//
// The parsing below deliberately mirrors adtc-profiler/src/adtc_profiler/gguf.py
// rather than being independently clever: the point is to predict what the
// evaluator will compute, not to be right in some other way.
package gguf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// GGUF value type ids, per the GGUF v2/v3 specification.
const (
	vtUint8 uint32 = iota
	vtInt8
	vtUint16
	vtInt16
	vtUint32
	vtInt32
	vtFloat32
	vtBool
	vtString
	vtArray
	vtUint64
	vtInt64
	vtFloat64
)

var scalarSize = map[uint32]int64{
	vtUint8: 1, vtInt8: 1,
	vtUint16: 2, vtInt16: 2,
	vtUint32: 4, vtInt32: 4,
	vtFloat32: 4, vtBool: 1,
	vtUint64: 8, vtInt64: 8, vtFloat64: 8,
}

// Guard rails matching the reference implementation, so a corrupt or hostile
// header cannot make this loop for a very long time.
const (
	maxTensors = 1 << 16
	maxDims    = 8
	maxDim     = 1 << 40
	maxKVs     = 1 << 20
	maxStrLen  = 1 << 22
)

// Info is what the evaluator will see.
type Info struct {
	Architecture  string
	ContextLength uint64
	ParamsCount   uint64
	// ParamsFromKV is true when general.parameter_count was present, false when
	// the count came from summing the tensor table.
	ParamsFromKV bool
	TensorCount  uint64
	KVCount      uint64
	Version      uint32
	FileBytes    int64
	Quantization string
}

// ErrNotGGUF means the file does not begin with the GGUF magic.
var ErrNotGGUF = errors.New("not a GGUF file (bad magic)")

type reader struct {
	f   *os.File
	buf [8]byte
	pos int64
}

func (r *reader) read(n int64) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(r.f, b); err != nil {
		return nil, err
	}
	r.pos += n
	return b, nil
}

func (r *reader) u32() (uint32, error) {
	if _, err := io.ReadFull(r.f, r.buf[:4]); err != nil {
		return 0, err
	}
	r.pos += 4
	return binary.LittleEndian.Uint32(r.buf[:4]), nil
}

func (r *reader) u64() (uint64, error) {
	if _, err := io.ReadFull(r.f, r.buf[:8]); err != nil {
		return 0, err
	}
	r.pos += 8
	return binary.LittleEndian.Uint64(r.buf[:8]), nil
}

func (r *reader) str() (string, error) {
	n, err := r.u64()
	if err != nil {
		return "", err
	}
	if n > maxStrLen {
		return "", fmt.Errorf("implausible string length %d", n)
	}
	b, err := r.read(int64(n))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (r *reader) skip(n int64) error {
	if n < 0 {
		return fmt.Errorf("negative skip %d", n)
	}
	pos, err := r.f.Seek(n, io.SeekCurrent)
	if err != nil {
		return err
	}
	r.pos = pos
	return nil
}

// value reads one KV value, returning it only for the types we care about.
func (r *reader) value(vt uint32) (any, error) {
	if sz, ok := scalarSize[vt]; ok {
		b, err := r.read(sz)
		if err != nil {
			return nil, err
		}
		switch vt {
		case vtUint32:
			return uint64(binary.LittleEndian.Uint32(b)), nil
		case vtInt32:
			return uint64(int32(binary.LittleEndian.Uint32(b))), nil
		case vtUint64:
			return binary.LittleEndian.Uint64(b), nil
		case vtInt64:
			return uint64(int64(binary.LittleEndian.Uint64(b))), nil
		case vtUint16:
			return uint64(binary.LittleEndian.Uint16(b)), nil
		case vtBool:
			return b[0] != 0, nil
		}
		return nil, nil
	}
	switch vt {
	case vtString:
		return r.str()
	case vtArray:
		elem, err := r.u32()
		if err != nil {
			return nil, err
		}
		count, err := r.u64()
		if err != nil {
			return nil, err
		}
		if sz, ok := scalarSize[elem]; ok {
			// One seek rather than count reads: token-score arrays run to
			// hundreds of thousands of elements.
			return nil, r.skip(int64(count) * sz)
		}
		for i := uint64(0); i < count; i++ {
			if _, err := r.value(elem); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unknown GGUF value type %d", vt)
}

// Read parses the header of the GGUF at path.
func Read(path string) (*Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}

	r := &reader{f: f}
	magic, err := r.read(4)
	if err != nil {
		return nil, err
	}
	if string(magic) != "GGUF" {
		return nil, ErrNotGGUF
	}

	info := &Info{FileBytes: st.Size()}
	if info.Version, err = r.u32(); err != nil {
		return nil, err
	}
	// v1 used 32-bit counts; this parser reads the v2/v3 64-bit layout, so
	// accepting v1 would yield misaligned garbage. The profiler rejects it too.
	if info.Version != 2 && info.Version != 3 {
		return nil, fmt.Errorf("unsupported GGUF version %d (profiler accepts 2 and 3)", info.Version)
	}
	if info.TensorCount, err = r.u64(); err != nil {
		return nil, err
	}
	if info.KVCount, err = r.u64(); err != nil {
		return nil, err
	}
	if info.KVCount > maxKVs {
		return nil, fmt.Errorf("implausible kv count %d", info.KVCount)
	}

	for i := uint64(0); i < info.KVCount; i++ {
		key, err := r.str()
		if err != nil {
			return nil, fmt.Errorf("kv %d key: %w", i, err)
		}
		vt, err := r.u32()
		if err != nil {
			return nil, fmt.Errorf("kv %q type: %w", key, err)
		}
		val, err := r.value(vt)
		if err != nil {
			return nil, fmt.Errorf("kv %q value: %w", key, err)
		}
		switch {
		case key == "general.parameter_count":
			if n, ok := val.(uint64); ok {
				info.ParamsCount = n
				info.ParamsFromKV = true
			}
		case key == "general.architecture":
			if s, ok := val.(string); ok {
				info.Architecture = s
			}
		case key == "general.file_type":
			if n, ok := val.(uint64); ok {
				info.Quantization = fileType(n)
			}
		case strings.HasSuffix(key, ".context_length"):
			if n, ok := val.(uint64); ok {
				info.ContextLength = n
			}
		}
	}

	if !info.ParamsFromKV {
		n, err := sumTensorParams(r, info.TensorCount)
		if err != nil {
			return nil, err
		}
		info.ParamsCount = n
	}
	return info, nil
}

// sumTensorParams walks the tensor-info section and totals element counts.
func sumTensorParams(r *reader, nTensors uint64) (uint64, error) {
	if nTensors == 0 || nTensors > maxTensors {
		return 0, fmt.Errorf("implausible tensor count %d", nTensors)
	}
	var total uint64
	for i := uint64(0); i < nTensors; i++ {
		if _, err := r.str(); err != nil { // tensor name
			return 0, fmt.Errorf("tensor %d name: %w", i, err)
		}
		nDims, err := r.u32()
		if err != nil {
			return 0, err
		}
		if nDims == 0 || nDims > maxDims {
			return 0, fmt.Errorf("tensor %d has %d dims", i, nDims)
		}
		count := uint64(1)
		for d := uint32(0); d < nDims; d++ {
			dim, err := r.u64()
			if err != nil {
				return 0, err
			}
			if dim == 0 || dim > maxDim {
				return 0, fmt.Errorf("tensor %d dim %d = %d", i, d, dim)
			}
			count *= dim
		}
		if _, err := r.u32(); err != nil { // ggml type
			return 0, err
		}
		if _, err := r.u64(); err != nil { // data offset
			return 0, err
		}
		total += count
	}
	return total, nil
}

// fileType maps GGUF general.file_type to its conventional label.
func fileType(n uint64) string {
	switch n {
	case 0:
		return "F32"
	case 1:
		return "F16"
	case 2:
		return "Q4_0"
	case 3:
		return "Q4_1"
	case 7:
		return "Q8_0"
	case 8:
		return "Q5_0"
	case 9:
		return "Q5_1"
	case 10:
		return "Q2_K"
	case 11:
		return "Q3_K_S"
	case 12:
		return "Q3_K_M"
	case 13:
		return "Q3_K_L"
	case 14:
		return "Q4_K_S"
	case 15:
		return "Q4_K_M"
	case 16:
		return "Q5_K_S"
	case 17:
		return "Q5_K_M"
	case 18:
		return "Q6_K"
	default:
		return "type_" + strconv.FormatUint(n, 10)
	}
}

// ParseEstimate converts "135M", "1.8B", "7B" to an integer, mirroring the
// profiler's parse_parameter_estimate.
func ParseEstimate(s string) (uint64, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	for suffix, mult := range map[string]float64{"B": 1e9, "M": 1e6, "K": 1e3} {
		if strings.HasSuffix(s, suffix) {
			f, err := strconv.ParseFloat(strings.TrimSuffix(s, suffix), 64)
			if err != nil {
				return 0, false
			}
			return uint64(f * mult), true
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// FraudCheck replicates adtc_profiler.gguf.fraud_check: measured parameters must
// fall within ±15% of the claimed estimate. The third return value is false when
// the check cannot be performed, matching the profiler's tri-state None.
func FraudCheck(claimed string, actual uint64) (pass bool, ok bool) {
	if actual == 0 {
		return false, false
	}
	c, valid := ParseEstimate(claimed)
	if !valid || c == 0 {
		return false, false
	}
	lo := float64(c) * 0.85
	hi := float64(c) * 1.15
	a := float64(actual)
	return a >= lo && a <= hi, true
}

// SuggestEstimate returns the shortest one-decimal "N.NB" label that passes
// FraudCheck for the measured count, so metadata.json can be made truthful
// against the artifact rather than against the upstream model card.
func SuggestEstimate(actual uint64) string {
	if actual == 0 {
		return ""
	}
	b := float64(actual) / 1e9
	for _, cand := range []string{
		strconv.FormatFloat(roundTo(b, 1), 'f', -1, 64) + "B",
		strconv.FormatFloat(roundTo(b, 2), 'f', -1, 64) + "B",
	} {
		if pass, ok := FraudCheck(cand, actual); ok && pass {
			return cand
		}
	}
	return strconv.FormatFloat(b, 'f', 2, 64) + "B"
}

func roundTo(f float64, places int) float64 {
	mult := 1.0
	for i := 0; i < places; i++ {
		mult *= 10
	}
	return float64(int64(f*mult+0.5)) / mult
}
