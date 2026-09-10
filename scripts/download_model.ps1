# download_model.ps1 — Windows equivalent of download_model.sh
#
# Idempotent fetcher for the Kanzu Agent GGUF checkpoint (Qwen2.5-1.5B Q4_K_M).
# Public URL, no credentials. Safe to re-run; skips download when the file
# already matches the expected SHA-256.
#
# Usage (from the repo root, PowerShell):
#   powershell -ExecutionPolicy Bypass -File scripts\download_model.ps1
#
# Model provenance:
#   Base model:  Qwen/Qwen2.5-1.5B-Instruct
#   Quantized by: bartowski (GGUF Q4_K_M)
#   Source: https://huggingface.co/bartowski/Qwen2.5-1.5B-Instruct-GGUF

$ErrorActionPreference = "Stop"

$RepoRoot = Split-Path -Parent $PSScriptRoot
$ModelDir = Join-Path $RepoRoot "model"
$ModelPath = Join-Path $ModelDir "Qwen2.5-1.5B-Instruct-Q4_K_M.gguf"

$ExpectedSha256 = "1adf0b11065d8ad2e8123ea110d1ec956dab4ab038eab665614adba04b6c3370"
$ExpectedBytes = 986048768

$ModelUrl = "https://huggingface.co/bartowski/Qwen2.5-1.5B-Instruct-GGUF/resolve/main/Qwen2.5-1.5B-Instruct-Q4_K_M.gguf"
$FallbackUrl = "https://hf.co/bartowski/Qwen2.5-1.5B-Instruct-GGUF/resolve/main/Qwen2.5-1.5B-Instruct-Q4_K_M.gguf"

function Get-FileSha256([string]$Path) {
    if (-not (Test-Path $Path)) { return $null }
    try {
        return (Get-FileHash -Algorithm SHA256 -Path $Path).Hash.ToLowerInvariant()
    } catch {
        return $null
    }
}

function Test-ModelPresent {
    if (-not (Test-Path $ModelPath)) { return $false }
    $sha = Get-FileSha256 $ModelPath
    if ($sha -ne $ExpectedSha256) {
        Write-Host "WARN: sha256 mismatch ($sha != $ExpectedSha256). Will re-download."
        return $false
    }
    $bytes = (Get-Item $ModelPath).Length
    if ($bytes -ne $ExpectedBytes) {
        Write-Host "WARN: byte count mismatch ($bytes != $ExpectedBytes). Will re-download."
        return $false
    }
    return $true
}

function Invoke-Download([string]$Url) {
    Write-Host "Downloading Qwen2.5-1.5B-Instruct-Q4_K_M.gguf from Hugging Face ..."
    Write-Host "  URL:  $Url"
    Write-Host "  Dest: $ModelPath"
    Write-Host "  Expected sha256: $ExpectedSha256"
    Write-Host "  Expected bytes:  $ExpectedBytes"
    Write-Host ""
    # TLS 1.2+ required by Hugging Face on older Windows PowerShell.
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -Uri $Url -OutFile $ModelPath -UseBasicParsing
}

function Test-Downloaded {
    Write-Host ""
    Write-Host "Verifying download ..."
    if (-not (Test-Path $ModelPath)) {
        Write-Error "model file not found at $ModelPath after download"
        exit 1
    }
    $sha = Get-FileSha256 $ModelPath
    if ($sha -ne $ExpectedSha256) {
        Write-Host "ERROR: sha256 mismatch."
        Write-Host "  Expected: $ExpectedSha256"
        Write-Host "  Actual:   $sha"
        Write-Host "  The file may be corrupt. Delete it and re-run this script."
        exit 1
    }
    $bytes = (Get-Item $ModelPath).Length
    Write-Host "  sha256:  $sha  (match)"
    Write-Host "  bytes:   $bytes  (expected $ExpectedBytes)"
    Write-Host ""
    Write-Host "Model ready: $ModelPath"
    Write-Host "Next: .\bin\kanzu.exe doctor"
}

# --- main ---
if (Test-ModelPresent) {
    Write-Host "Model already present and verified at $ModelPath"
    Write-Host "  sha256: $ExpectedSha256"
    Write-Host "  bytes:  $ExpectedBytes"
    exit 0
}

New-Item -ItemType Directory -Force -Path $ModelDir | Out-Null

try {
    Invoke-Download $ModelUrl
} catch {
    Write-Host "Primary URL failed; trying fallback ..."
    try {
        Invoke-Download $FallbackUrl
    } catch {
        Write-Host "ERROR: both URLs failed. Check network and HF repo visibility."
        Write-Host $_.Exception.Message
        exit 1
    }
}

Test-Downloaded
