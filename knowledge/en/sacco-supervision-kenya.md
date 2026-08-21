---
lang: en
source: sacco-supervision-kenya
citation: Proceeds of Crime and Anti-Money Laundering Act, 2009 (Kenya) and the Financial Reporting Centre established under it; Prevention of Terrorism Act, 2012; Sacco Societies Act, 2008 and SASRA as the supervisory authority
---

# Supervisory context for Kenyan SACCOs

## Which instruments apply

Kenya's principal anti-money-laundering statute is the Proceeds of Crime and
Anti-Money Laundering Act, 2009, commonly POCAMLA. It creates the offence of money
laundering, imposes obligations on reporting institutions, and establishes the
Financial Reporting Centre as the national financial intelligence unit. Terrorist
financing is addressed principally through the Prevention of Terrorism Act, 2012.

Prudential supervision of deposit-taking SACCOs sits with the SACCO Societies
Regulatory Authority under the Sacco Societies Act, 2008. A deposit-taking SACCO
therefore answers to SASRA for prudential matters and reports suspicious
transactions to the Financial Reporting Centre. These are separate obligations to
separate bodies and satisfying one does not satisfy the other.

Specific numeric thresholds, prescribed forms and filing deadlines are set in
subsidiary regulations and are amended from time to time. They must be read from
the current instrument. Any figure embedded in software should be a configured
institutional parameter that an officer can update, and every alert should record
the figure it was measured against so historical findings remain interpretable
after a change.

## Obligations of a reporting institution

The recurring obligations are to conduct customer due diligence and keep it
current; to monitor the relationship on an ongoing basis; to keep records
sufficient to reconstruct transactions; to report suspicious transactions promptly
to the Financial Reporting Centre; to file the prescribed reports for cash
transactions above the prescribed value; to appoint a compliance officer; to train
staff; and to submit the programme to independent audit.

Reporting in good faith is protected. Disclosing to the member that a report has
been made or contemplated is prohibited.

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

Staff numbers are small, so the compliance officer, the person who processed the
transaction and the person reviewing it may be the same individual. Segregation of
duties cannot always be achieved by headcount, so it has to be approximated by
system controls: immutable logs, deterministic rules that cannot be quietly
overridden, and alerts that persist until someone records a disposition.

## Why offline capability is a compliance requirement here

Connectivity in much of the SACCO branch network is intermittent and metered.
Where a monitoring system requires a live connection, monitoring simply stops when
the link drops, and the institution's obligation does not stop with it. The gap is
then reconstructed later from memory, if at all.

A monitoring system that runs entirely on the branch machine keeps detection and
record-keeping continuous through an outage, and defers only the transmission of
reports, which is the part that genuinely requires a network. This also keeps
member financial data on the institution's own hardware, which is the
straightforward way to satisfy data-protection expectations without negotiating a
cross-border transfer basis for a cloud processor.

## Disposition of an alert

An alert is not a report. Each alert must reach one of three recorded outcomes: it
is escalated to a suspicious transaction report; it is dismissed with a recorded
reason; or it remains open with an owner and a review date. An alert store that
permits an alert to simply age out of view is a control failure, because the
absence of a decision is indistinguishable from a decision not to act.

The reasoning behind a dismissal is the document a supervisor will ask for. It
should name the evidence that resolved the concern, such as documentation of the
source of funds, and not merely record a conclusion.
