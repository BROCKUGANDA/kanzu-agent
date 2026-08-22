---
lang: en
source: typologies-east-africa
citation: FATF Guidance on AML/CFT and Financial Inclusion; FATF Recommendations R.12 (PEPs), R.13 (correspondent), R.16 (wire transfers), R.19 (higher-risk countries)
---

# Money-laundering typologies seen in East African cooperatives

## Structuring (deposit splitting, smurfing)

A member who would trigger a reporting obligation with a single deposit instead
makes several smaller ones, each below the reporting value, often across a few
days and sometimes across several branches or agents.

The detectable signature is an aggregate that breaches the threshold assembled
from components that individually do not, inside a short window. Two data elements
establish it: the timestamps, which show the deposits are one episode rather than
ordinary trading receipts, and the individual amounts, which cluster below the
line rather than scattering around it.

Structuring is a distinct offence in most jurisdictions, separate from the
laundering itself. It is also evidence of knowledge: someone who splits deposits
to stay under a threshold knows where the threshold is. That inference is
significant enough that the aggregation window and the threshold used must both be
recorded on the alert, because a later change to institutional policy would
otherwise make the historical finding uninterpretable.

## Threshold hugging

Related to structuring but distinct, and often missed because the aggregate does
not breach anything. Amounts cluster immediately below the reporting value — the
high eighties to high nineties as a percentage of it — without the total crossing
the line.

On its own this is weak evidence and merits a low severity. Its value is
correlative: co-occurring with structuring, or with a member whose stated
occupation does not generate such receipts, it moves an assessment materially.

## Mobile money and agent-banking layering

Mobile wallets and agent networks are the principal delivery channel for financial
inclusion in the region and are correspondingly attractive for layering. The
pattern is value arriving into an account and most of it leaving again almost
immediately, frequently through a different channel from the one it arrived on.

The signature is a high ratio of outflow to recent inflow inside a short window.
A savings product being used with near-total same-window outflow is not being used
as a savings product; it is being used as a conduit. Channel switching between the
inbound and outbound legs strengthens the reading, because it frustrates simple
same-rail tracing.

Agent-originated deposits deserve particular attention: the agent, not the
institution, performs the face-to-face identification, so the reliability of the
CDD depends on a third party whose incentives are transaction volume.

## Dormant account reactivation

An account with no activity for an extended period suddenly receives material
value. Dormant accounts are attractive precisely because they carry an
unremarkable history and an established, already-verified identity.

Two variants matter. In the first the genuine member has been recruited or paid to
allow use of the account. In the second the account has been taken over without
the member's knowledge, in which case the member is a victim and the institution's
duty runs to them as well as to the regulator. The finding is the same; the
follow-up is not, and the review must not assume the first variant.

## Repeated round-figure amounts

Genuine commerce produces untidy numbers: receipts carry odd shillings, invoices
carry discounts, market takings vary day to day. Repeated identical round figures
suggest a schedule rather than trading activity — a placement plan executed to a
fixed instruction rather than money arriving as it is earned.

Weak on its own, since payroll, standing contributions and loan repayments are also
round and repeated. It becomes meaningful when the narrative does not explain the
regularity, or when the amounts are large relative to the member's profile.

## Velocity and volume spikes

An abrupt rise in transaction count or value relative to the member's own trailing
baseline. The essential design point is that the baseline must be per-member: an
institution-wide average generates false positives on every legitimately active
trader and false negatives on every small account being used as a conduit.

A baseline needs enough history to be meaningful. Firing a velocity rule on a
member's first active week measures nothing except that they have started using
the account.

## Higher-risk jurisdiction exposure

Recommendation 19 requires enhanced due diligence for business relationships and
transactions connected to countries for which FATF calls for such measures. The
relevant lists are maintained by FATF and updated at each plenary; an institution
carrying a stale watchlist is not compliant, so the list belongs in configuration
that is reviewed on a schedule, not in code.

Exposure to a listed jurisdiction is a trigger for enhanced scrutiny. It is not by
itself evidence of wrongdoing, and remittance corridors serving diaspora families
routinely touch listed countries for entirely ordinary reasons. The finding
mandates a documented review, and its wording should not imply more than that.

Recommendation 16 additionally requires that cross-border transfers carry accurate
originator and beneficiary information throughout the payment chain. Missing or
meaningless originator data on an inbound transfer is itself a red flag.

## Politically exposed persons

Recommendation 12 requires institutions to determine whether a member or a
beneficial owner is a politically exposed person, and where so, to obtain senior
management approval, establish the source of wealth and source of funds, and apply
enhanced ongoing monitoring.

PEP status is a risk classification and not an allegation. In a Ugandan cooperative
context the relevant PEPs are frequently local: Members of Parliament, district
chairpersons, Resident District Commissioners, board members of public bodies,
and senior officers of other cooperatives. Domestic PEPs attract the same enhanced
monitoring duty as foreign ones where the risk assessment supports it.

## What a suspicious activity note should and should not say

It should state the observed pattern, the value involved, the transaction
identifiers that evidence it, the window over which it was assembled, the threshold
or baseline it was measured against, and the specific obligation the institution is
discharging by reporting.

It should not assert that an offence occurred, name a predicate crime, or draw a
legal conclusion. The institution reports suspicion and the facts supporting it;
determining criminality is for authorities. A note that overstates is not more
protective — it is a document that will be tested and found to have exceeded what
the evidence supported.
