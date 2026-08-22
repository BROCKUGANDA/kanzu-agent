---
lang: en
source: sacco-supervision-uganda
citation: Anti-Money Laundering Act, 2013 (Uganda) and the Financial Intelligence Authority established under it; Anti-Terrorism Act, 2002 (as amended); Tier 4 Microfinance Institutions and Money Lenders Act, 2016 and the Uganda Microfinance Regulatory Authority (UMRA) as the supervisory authority for SACCOs
---

# Supervisory context for Ugandan SACCOs

## Which instruments apply

Uganda's principal anti-money-laundering statute is the Anti-Money Laundering Act,
2013 (AMLA). It creates the offence of money laundering, imposes obligations on
accountable persons, and establishes the Financial Intelligence Authority (FIA) as
the national financial intelligence unit. Terrorist financing is addressed principally
through the Anti-Terrorism Act, 2002 and its subsequent amendments.

Prudential supervision of savings and credit cooperatives sits with the Uganda
Microfinance Regulatory Authority (UMRA) under the Tier 4 Microfinance Institutions
and Money Lenders Act, 2016. A deposit-taking SACCO therefore answers to UMRA for
prudential matters and reports suspicious transactions to the Financial Intelligence
Authority. These are separate obligations to separate bodies and satisfying one does
not satisfy the other.

Specific numeric thresholds, prescribed forms and filing deadlines are set in
subsidiary regulations and are amended from time to time. They must be read from
the current instrument. Any figure embedded in software should be a configured
institutional parameter that an officer can update, and every alert should record
the figure it was measured against so historical findings remain interpretable
after a change.

## Obligations of an accountable person

The recurring obligations under AMLA are to conduct customer due diligence and keep
it current; to monitor the relationship on an ongoing basis; to keep records
sufficient to reconstruct transactions; to report suspicious transactions promptly
to the Financial Intelligence Authority; to file the prescribed reports for cash
transactions above the prescribed threshold; to appoint a money-laundering compliance
officer; to train staff; and to submit the programme to independent audit.

Reporting in good faith is protected. Disclosing to the customer that a report has
been made or contemplated (tipping off) is a separate offence under AMLA.

## Where cooperative governance interacts with compliance

A SACCO's supervisory committee and board carry the governance duty, but detection
work happens at the branch and agent counter. Three structural features of
cooperative governance create specific compliance exposure.

Members are also owners, so the social cost of questioning a member's transaction
is higher than in a commercial bank and escalation is under informal pressure to
stop. Written procedure and an audit trail that records who reviewed what exists
partly to protect the officer who escalates.

Committee members are frequently prominent locally, which makes domestic PEP
identification an internal matter and not merely an external screening exercise.
In Uganda this includes local government officials, Members of Parliament, district
chairpersons, and senior officers of public bodies.

Staff numbers are small, so the compliance officer, the person who processed the
transaction and the person reviewing it may be the same individual. Segregation of
duties cannot always be achieved by headcount, so it has to be approximated by
system controls: immutable logs, deterministic rules that cannot be quietly
overridden, and alerts that persist until someone records a disposition.

## Why offline capability is a compliance requirement here

Connectivity across much of Uganda's SACCO branch network is intermittent and
metered. Internet penetration outside Kampala and major towns remains limited, with
many branches relying on mobile data that drops during peak hours or power outages.
Where a monitoring system requires a live connection, monitoring simply stops when
the link drops, and the institution's obligation does not stop with it. The gap is
then reconstructed later from memory, if at all.

A monitoring system that runs entirely on the branch machine keeps detection and
record-keeping continuous through an outage, and defers only the transmission of
reports, which is the part that genuinely requires a network. This also keeps
member financial data on the institution's own hardware, which is the
straightforward way to satisfy data-protection expectations under Uganda's Data
Protection and Privacy Act, 2019 without negotiating a cross-border transfer basis
for a cloud processor.

## Disposition of an alert

An alert is not a report. Each alert must reach one of three recorded outcomes: it
is escalated to a suspicious transaction report filed with the FIA; it is dismissed
with a recorded reason; or it remains open with an owner and a review date. An alert
store that permits an alert to simply age out of view is a control failure, because
the absence of a decision is indistinguishable from a decision not to act.

The reasoning behind a dismissal is the document a supervisor will ask for. It
should name the evidence that resolved the concern, such as documentation of the
source of funds, and not merely record a conclusion.
