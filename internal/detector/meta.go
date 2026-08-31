// Package detector holds the fixed text and metadata the Coordinated-Network
// Detector must present identically everywhere it appears.
//
// It exists because of one word in PRD 10.9.2: the standing disclaimer is
// required VERBATIM on every report and on the network detail page. A
// requirement to reproduce a paragraph exactly, in two renderers, cannot be met
// by two copies of the paragraph. The same reasoning extends to the per-signal
// method sentences (US50 requires them in the UI panel, PRD 10.8 item 3 requires
// them in the report) and to the known limitations PRD requires to be stated
// rather than left implicit.
//
// Nothing here touches the database or the network. It is the vocabulary the
// service layer and the report generator share.
package detector

// Disclaimer is PRD 10.9.2's standing disclaimer, reproduced verbatim.
//
// Required on EVERY report and on the network detail page. On the report it
// appears twice by design — in full on the cover page (PRD 10.8 item 1), so it
// cannot be missed, and again as the closing "Limitations and disclaimer"
// section (item 9). Do not deduplicate those two.
//
// Do not paraphrase, summarise, or shorten this text. It is the sentence that
// stands between a behavioural observation and an accusation.
const Disclaimer = "This report documents statistical patterns in publicly available account " +
	"behaviour — the timing, duplication, and provenance characteristics of a set of accounts " +
	"within a defined time window. It is not a determination that any account is automated, " +
	"inauthentic, or operated in bad faith, and it makes no claim about the identity, " +
	"affiliation, or intent of any account holder. Coordinated posting behaviour has legitimate " +
	"explanations, including organised civic campaigns, newsroom syndication, and community " +
	"mobilisation in response to real events. Findings require human assessment before any " +
	"action is taken."

// Cluster metric codes (PRD 10.5.5).
const (
	SignalSY = "SY"
	SignalDU = "DU"
	SignalCO = "CO"
	SignalPR = "PR"
	SignalAU = "AU"
)

// Pairwise signal family keys (PRD 10.5.2), as recorded in
// detection_run.signals_unavailable.
const (
	FamilyTime   = "w_time"
	FamilyText   = "w_text"
	FamilyAmp    = "w_amp"
	FamilyMeta   = "w_meta"
	FamilyStruct = "w_struct"
)

// SignalMeta describes one cluster metric for display.
type SignalMeta struct {
	Code string
	Name string
	// Method is the one-sentence plain-language description US50 requires. The
	// constraint it has to satisfy is explicit in the story: "a policy reviewer
	// must be able to read this panel without knowing what conductance is." So
	// none of these sentences uses a term it does not immediately explain.
	Method string
	// Weight is the metric's share of the composite (PRD 10.5.5).
	Weight float64
	// Families are the pairwise signal families this metric derives from. Used
	// to decide whether a metric is measurable at all on a run where some
	// families were unavailable.
	//
	// CO has no family listed because cohesion is a property of the graph
	// rather than of any pairwise comparison — the same fact that makes its
	// role in SignalBreadth contested. See PRD-v1.4.md 4.5.
	Families []string
}

// SignalCatalogue is the five cluster metrics in composite order.
var SignalCatalogue = []SignalMeta{
	{
		Code:     SignalSY,
		Name:     "Synchrony",
		Method:   "How often these accounts posted at the same moments, measured against how often accounts posting at their individual rates would coincide by chance, plus how much of their activity fell inside unusually busy spikes.",
		Weight:   0.25,
		Families: []string{FamilyTime},
	},
	{
		Code:     SignalDU,
		Name:     "Duplication",
		Method:   "The share of this group's posts that repeat, word for word or lightly reworded, a post published by another member of the same group.",
		Weight:   0.25,
		Families: []string{FamilyText},
	},
	{
		Code:   SignalCO,
		Name:   "Cohesion",
		Method: "How much of this group's measured behavioural similarity is with each other rather than with accounts outside the group — densely connected inside, sparsely connected outside.",
		Weight: 0.20,
	},
	{
		Code:     SignalPR,
		Name:     "Provenance anomaly",
		Method:   "How many of these accounts were created within the same short window, share a handle pattern, or share a profile image, compared with the platform's ordinary baseline.",
		Weight:   0.15,
		Families: []string{FamilyMeta},
	},
	{
		Code:     SignalAU,
		Name:     "Automation and behavioural anomaly",
		Method:   "How regular the gaps between these accounts' posts are, whether they post around the clock without a rest period, and how much they reshare rather than write. These are observations about posting patterns, not a finding that any account is automated.",
		Weight:   0.15,
		Families: []string{FamilyAmp},
	},
}

// FamilyNames maps a signal family key onto the label shown to a reader.
var FamilyNames = map[string]string{
	FamilyTime:   "Temporal synchrony",
	FamilyText:   "Content duplication",
	FamilyAmp:    "Co-amplification",
	FamilyMeta:   "Provenance and identity similarity",
	FamilyStruct: "Structural overlap",
}

// FamilyLabel returns a family's display name, falling back to the raw key so
// a family the pipeline adds later still renders as something.
func FamilyLabel(key string) string {
	if name, ok := FamilyNames[key]; ok {
		return name
	}
	return key
}

// KnownLimitations are the caveats the PRD requires to be stated rather than
// left for a reader to infer.
//
// The first is PRD 10.5.2.1's, and it is stated as a requirement: accounts in
// the same timezone reacting to the same news event within the same minute show
// elevated synchrony. It belongs beside the signal scores in the UI and in the
// report's methodology appendix.
//
// The last is PRD 11's platform-coverage asymmetry. It is carried here because
// an empty network list on a city whose discourse runs through closed messaging
// channels is not evidence of no coordination, and reading it as reassurance is
// a specific, foreseeable misuse of this feature.
var KnownLimitations = []string{
	"Accounts in the same timezone reacting to the same news event within the same minute will show elevated synchrony. This is why synchrony alone can never form a cluster: every edge requires at least two independent signal families to agree.",
	"Coordinated posting has legitimate explanations. Organised civic campaigns, newsroom syndication, and community mobilisation after a real event all produce genuine coordination, which is why declared coordination is allowlisted rather than flagged.",
	"Accounts created within a short window of each other are not necessarily related. Platform signup surges follow real-world events, so provenance is weighted low and can never drive a finding on its own.",
	"Detection covers only platforms that expose the required behavioural signals. Closed messaging channels expose almost none of them, so an empty result for a claim does not establish that no coordination occurred there.",
}

// TruncationNote is shown wherever a network from a truncated run is displayed.
//
// PRD 10.5.1 requires the truncation to be displayed, not merely recorded: a
// truncated run has known incomplete recall, and the analyst has to be told at
// the point of judgement rather than left to find it in run metadata.
const TruncationNote = "This run's candidate set exceeded the configured cap and was truncated to the " +
	"highest-volume accounts. Recall is known to be incomplete: accounts that belong to this network may " +
	"be missing from it, and other networks on this claim may not have been detected at all."

// ConfidenceCapNote explains PRD 10.6.3 rule 4 where it applied.
const ConfidenceCapNote = "Every network from this run is capped at Medium confidence regardless of score, " +
	"because the run either truncated its candidate set or could not measure two or more signal families."

// BreadthGuardNote is PRD 10.6.2's rationale, carried wherever a band is shown.
const BreadthGuardNote = "A high composite score with only one agreeing signal family can never reach High " +
	"confidence. That configuration is the characteristic shape of a false positive, not of a campaign."

// AvailabilityPublic and AvailabilityDeleted are the two labels US54 renders
// against a snapshotted post.
//
// Constants rather than inline strings so the marker reads identically in the
// UI and in the PDF, which is the same reason the disclaimer lives here.
const (
	AvailabilityPublic  = "Publicly available at capture time"
	AvailabilityDeleted = "No longer publicly available"
)

// Availability returns the label for a snapshotted post.
func Availability(stillPublic bool) string {
	if stillPublic {
		return AvailabilityPublic
	}
	return AvailabilityDeleted
}

// PrecisionTarget is PRD 10.9.3's recommended operational target: precision
// above 0.85 on analyst-confirmed networks, on a rolling 90-day basis.
//
// Recall is deliberately secondary, and the PRD says why: "a missed network
// costs a missed referral; a false positive costs a government publicly
// implying that residents are bots."
const PrecisionTarget = 0.85

// PrecisionWindowDays is the rolling window the target is measured over.
const PrecisionWindowDays = 90

// GraphLegibilityLimit is US51's node ceiling. Beyond it the graph is rendered
// as its k-core and the reduction is stated.
const GraphLegibilityLimit = 300
