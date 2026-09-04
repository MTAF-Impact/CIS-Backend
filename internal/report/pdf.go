package report

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"

	"github.com/cis/cis-backend/internal/detector"
	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/models"
)

// CIS palette. The report is a government document rendered in the product's
// own colours: Regal Navy headers and Sea Green section rules.
var (
	colRegalNavy = [3]int{0x1C, 0x35, 0x7F}
	colSeaGreen  = [3]int{0x22, 0x91, 0x56}
	colPaleSky   = [3]int{0xC0, 0xD9, 0xE2}
	colMintCream = [3]int{0xF5, 0xFB, 0xFA}
	colGold      = [3]int{0xFB, 0xD3, 0x0A}
	colBodyText  = [3]int{0x1A, 0x1A, 0x1A}
	colMutedText = [3]int{0x4A, 0x4A, 0x4A}
	colGlaucous  = [3]int{0x77, 0x85, 0xB3}
)

// A4 portrait geometry in millimetres.
const (
	pageWidth   = 210.0
	marginLeft  = 18.0
	marginRight = 18.0
	marginTop   = 20.0
	// marginBottom leaves room for the two-line footer, which carries the
	// report id, network id, page number and BOTH timestamps on every page.
	marginBottom = 22.0
	contentWidth = pageWidth - marginLeft - marginRight
)

// Render produces the ten-section report PDF.
func Render(d Data) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")

	// Determinism, in four settings. See the package comment.
	pdf.SetCompression(true)
	pdf.SetCatalogSort(true)
	pdf.SetCreationDate(d.GeneratedAt)
	pdf.SetModificationDate(d.GeneratedAt)
	pdf.SetProducer("CIS Climate Immune System", false)
	pdf.SetTitle(fmt.Sprintf("Coordinated Network Report %s", d.Network.ID), false)

	pdf.SetMargins(marginLeft, marginTop, marginRight)
	pdf.SetAutoPageBreak(true, marginBottom)
	pdf.AliasNbPages("{nb}")
	pdf.SetFooterFunc(footerFunc(pdf, d))

	renderCover(pdf, d)
	renderExecutiveSummary(pdf, d)
	renderDetectionBasis(pdf, d)
	renderActivityTimeline(pdf, d)
	if d.Sections.Graph {
		renderNetworkStructure(pdf, d)
	}
	if d.Sections.ContentClusters {
		renderRepresentativeContent(pdf, d)
	}
	if d.Sections.AccountAnnex {
		renderAccountAnnex(pdf, d)
	}
	if d.Type == models.ReportTypeInternalBriefing {
		renderInternalContext(pdf, d)
	}
	if d.Sections.Methodology {
		renderMethodology(pdf, d)
	}
	renderLimitations(pdf, d)
	renderChainOfCustody(pdf, d)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// footerFunc renders the per-page footer: report ID, network ID, page number,
// and the generation timestamp in UTC AND city-local time.
//
// Both timestamps, on every page. It is the kind of requirement that looks like
// formatting and is not: a document circulated between a city government and a
// platform's trust-and-safety team crosses timezones, and "generated at 09:00"
// is ambiguous evidence.
func footerFunc(pdf *fpdf.Fpdf, d Data) func() {
	utc := d.GeneratedAt.UTC().Format("2006-01-02 15:04 MST")
	local := utc
	if d.CityLocation != nil {
		local = d.GeneratedAt.In(d.CityLocation).Format("2006-01-02 15:04 MST")
	}

	return func() {
		pdf.SetY(-16)
		pdf.SetDrawColor(colPaleSky[0], colPaleSky[1], colPaleSky[2])
		pdf.SetLineWidth(0.2)
		pdf.Line(marginLeft, pdf.GetY(), pageWidth-marginRight, pdf.GetY())

		pdf.Ln(1.5)
		pdf.SetFont("Helvetica", "", 7)
		pdf.SetTextColor(colMutedText[0], colMutedText[1], colMutedText[2])

		pdf.CellFormat(contentWidth*0.62, 4,
			tr(fmt.Sprintf("Report %s  |  Network %s", d.ReportID, d.Network.ID)), "", 0, "L", false, 0, "")
		pdf.CellFormat(contentWidth*0.38, 4,
			tr(fmt.Sprintf("Page %d of {nb}", pdf.PageNo())), "", 1, "R", false, 0, "")

		pdf.CellFormat(contentWidth, 4,
			tr(fmt.Sprintf("Generated %s (UTC) / %s (city local)", utc, local)), "", 1, "L", false, 0, "")
	}
}

// renderCover renders the report's cover page.
//
// The standing disclaimer is printed IN FULL on the cover, not buried at the
// back — it is the single most important piece of typography in the document:
// a reader who opens this file and reads nothing else must still learn that it
// is not a determination that any account is automated.
func renderCover(pdf *fpdf.Fpdf, d Data) {
	pdf.AddPage()

	pdf.SetFillColor(colRegalNavy[0], colRegalNavy[1], colRegalNavy[2])
	pdf.Rect(0, 0, pageWidth, 42, "F")

	pdf.SetY(14)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFont("Helvetica", "B", 20)
	pdf.CellFormat(contentWidth, 9, tr("Coordinated Network Report"), "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 10)
	pdf.CellFormat(contentWidth, 6, tr(reportTypeLabel(d.Type)), "", 1, "L", false, 0, "")

	pdf.SetY(52)
	pdf.SetTextColor(colBodyText[0], colBodyText[1], colBodyText[2])

	generatedBy := d.GeneratedBy
	if d.RedactAnalystNames || generatedBy == "" {
		generatedBy = "[redacted]"
	}

	local := d.GeneratedAt.UTC().Format("2006-01-02 15:04 MST")
	if d.CityLocation != nil {
		local = d.GeneratedAt.In(d.CityLocation).Format("2006-01-02 15:04 MST")
	}

	rows := [][2]string{
		{"Report ID", d.ReportID},
		{"Issued by", orDash(d.Organisation)},
		{"Network ID", d.Network.ID},
		{"Network label", orDash(d.Network.Label)},
		{"Coordination Score", fmt.Sprintf("%.1f / 100", d.Network.CoordinationScore)},
		{"Confidence band", bandLabel(d.Network.ConfidenceBand) + fmt.Sprintf("  (SignalBreadth %d)", d.Network.SignalBreadth)},
		{"Review status", statusLabel(d.Network.ReviewStatus)},
		{"Accounts", strconv.Itoa(d.Network.AccountCount)},
		{"Posts documented", strconv.Itoa(d.Network.PostCount)},
		{"Platforms", orDash(strings.Join(d.Network.Platforms, ", "))},
		{"Detection window", fmt.Sprintf("%s to %s",
			d.Network.Run.WindowStart.UTC().Format("2006-01-02 15:04 MST"),
			d.Network.Run.WindowEnd.UTC().Format("2006-01-02 15:04 MST"))},
		{"Detection run", d.RunID},
		{"Generated", fmt.Sprintf("%s (UTC) / %s (city local)", d.GeneratedAt.UTC().Format("2006-01-02 15:04 MST"), local)},
		{"Generated by", generatedBy},
	}
	for _, row := range rows {
		keyValue(pdf, row[0], row[1])
	}

	// A truncated run is stated on the cover, not left to the methodology
	// appendix: for a document read once by a stranger, the caveat belongs at
	// the point of judgement.
	if d.Network.Run.Truncated {
		pdf.Ln(3)
		calloutBox(pdf, colGold, "Known incomplete recall", detector.TruncationNote)
	}

	pdf.Ln(4)
	calloutBox(pdf, colSeaGreen, "Important — please read before acting on this report", detector.Disclaimer)
}

// renderExecutiveSummary renders a plain-language summary, at most 200 words,
// from a fixed template with numeric slots.
//
// Deliberately not generated by a model: the same detection must always
// produce the same summary, and the summary must never assert more than the
// data supports — both properties fail the moment a language model is in the
// loop, and the second is the one that matters.
func renderExecutiveSummary(pdf *fpdf.Fpdf, d Data) {
	sectionHeading(pdf, "1. Executive summary")

	n := d.Network
	claim := "a climate claim tracked by this system"
	if n.PrimaryClaim != nil {
		claim = fmt.Sprintf("the claim %q", truncateWords(n.PrimaryClaim.ClaimStatement, 25))
	}

	var b strings.Builder
	fmt.Fprintf(&b,
		"Between %s and %s, %d accounts posting about %s were observed behaving as a single unit. "+
			"They published %d posts in that window. ",
		n.Run.WindowStart.UTC().Format("2 January 2006"),
		n.Run.WindowEnd.UTC().Format("2 January 2006"),
		n.AccountCount, claim, n.PostCount)

	fmt.Fprintf(&b,
		"The behaviour scored %.0f out of 100 on the Coordination Score, at %s confidence, "+
			"with %d independent signal families in agreement. ",
		n.CoordinationScore, strings.ToLower(bandLabel(n.ConfidenceBand)), n.SignalBreadth)

	if strongest := strongestSignal(n.WhyFlagged.Signals); strongest != nil {
		fmt.Fprintf(&b, "The strongest single indicator was %s, at %.0f out of 100. ",
			strings.ToLower(strongest.Name), strongest.Score)
	}

	if n.Recurrence.Count > 1 {
		fmt.Fprintf(&b, "This set of accounts has been observed %d times since %s. ",
			n.Recurrence.Count, firstSeenLabel(n.Recurrence))
	}

	b.WriteString(
		"These are measurements of posting behaviour. They are not a finding that any account is " +
			"automated or operated in bad faith, and they require human assessment before any action is taken.")

	body(pdf, capWords(b.String(), ExecutiveSummaryWordLimit))
}

// renderDetectionBasis renders the full signal breakdown: raw counts,
// SignalBreadth, the confidence rule applied, the unavailable families, and
// the claim-relevance figures.
func renderDetectionBasis(pdf *fpdf.Fpdf, d Data) {
	sectionHeading(pdf, "2. Detection basis")

	body(pdf, "Each measure below is scored from 0 to 100 and combined into the Coordination Score using the "+
		"weights shown. Every score is accompanied by the observation behind it.")
	pdf.Ln(2)

	for _, sig := range d.Network.WhyFlagged.Signals {
		signalBlock(pdf, sig)
	}

	pdf.Ln(2)
	subHeading(pdf, "Composite and confidence")
	keyValue(pdf, "Coordination Score", fmt.Sprintf("%.1f / 100", d.Network.CoordinationScore))
	keyValue(pdf, "SignalBreadth", strconv.Itoa(d.Network.SignalBreadth))
	keyValue(pdf, "Confidence band", bandLabel(d.Network.ConfidenceBand))
	keyValue(pdf, "Rule applied", d.Network.WhyFlagged.Confidence.Rule)
	keyValue(pdf, "Internal density", fmt.Sprintf("%.3f", d.Network.WhyFlagged.InternalDensity))
	keyValue(pdf, "Conductance", fmt.Sprintf("%.3f", d.Network.WhyFlagged.Conductance))
	keyValue(pdf, "Comparison accounts", strconv.Itoa(d.Network.WhyFlagged.ComparisonAccountCount))

	// Stated as an explicit list, never silently omitted: the pipeline degrades
	// gracefully when a signal family is unavailable, and the run metadata
	// records the omission so the report can name which signals were
	// unavailable rather than silently under-reporting.
	unavailable := "None — all signal families were measured"
	if len(d.Network.WhyFlagged.SignalsUnavailable) > 0 {
		unavailable = strings.Join(d.Network.WhyFlagged.SignalsUnavailable, ", ")
	}
	keyValue(pdf, "Signals unavailable", unavailable)

	pdf.Ln(3)
	subHeading(pdf, "Claim relevance")
	body(pdf, "These figures establish that the cluster's activity is substantively about the claim under "+
		"report rather than incidental to it. A cluster that fails them is classified off-topic and is not reported.")

	rel := d.Network.WhyFlagged.ClaimRelevance
	if rel.PrimaryClaim != nil {
		keyValue(pdf, "Primary claim", truncateWords(rel.PrimaryClaim.ClaimStatement, 40))
		keyValue(pdf, "Overlap ratio",
			fmt.Sprintf("%.3f  (threshold %.2f)", rel.PrimaryClaim.OverlapRatio, rel.MinLinkStrengthThreshold))
		keyValue(pdf, "Member anchoring share",
			fmt.Sprintf("%.3f  (threshold %.2f)", rel.PrimaryClaim.AnchoringShare, rel.AnchorShareThreshold))
		keyValue(pdf, "Posts in claim cluster",
			fmt.Sprintf("%d  (threshold %d)", rel.PrimaryClaim.ClaimClusterPosts, rel.MinClaimPostsThreshold))
	}
	if len(rel.SecondaryClaims) > 0 {
		var secondary []string
		for _, c := range rel.SecondaryClaims {
			secondary = append(secondary, fmt.Sprintf("%s (overlap %.3f)", truncateWords(c.ClaimStatement, 12), c.OverlapRatio))
		}
		keyValue(pdf, "Secondary claim links", strings.Join(secondary, "; "))
	}

	// A recurrence inherits history but not relevance, so both the current
	// primary claim AND the prior anchoring claims are stated. That sentence —
	// "this same set of accounts previously amplified claims X and Y" — is what
	// makes a referral actionable, so it belongs in the detection basis rather
	// than in a footnote.
	if len(d.Network.Recurrence.PriorClaims) > 0 {
		pdf.Ln(3)
		subHeading(pdf, "Prior detections of this account set")
		body(pdf, fmt.Sprintf(
			"This set of accounts has been detected %d times. Each prior detection was assessed for relevance "+
				"against its own claim; recurrence does not carry relevance forward.", d.Network.Recurrence.Count))
		for _, p := range d.Network.Recurrence.PriorClaims {
			claim := "(claim no longer available)"
			if p.ClaimStatement != nil {
				claim = truncateWords(*p.ClaimStatement, 22)
			}
			bullet(pdf, fmt.Sprintf("%s — %s, score %.0f, %s",
				p.DetectedAt.UTC().Format("2 Jan 2006"), claim, p.CoordinationScore, bandLabel(p.ConfidenceBand)))
		}
	}
}

// signalBlock renders one metric with its meter, method sentence and raw counts.
func signalBlock(pdf *fpdf.Fpdf, sig dto.SignalDetail) {
	if pdf.GetY() > 240 {
		pdf.AddPage()
	}

	pdf.SetFont("Helvetica", "B", 9.5)
	pdf.SetTextColor(colRegalNavy[0], colRegalNavy[1], colRegalNavy[2])
	label := fmt.Sprintf("%s — %s", sig.Code, sig.Name)
	if !sig.Available {
		label += "  (not measurable this run)"
	}
	pdf.CellFormat(contentWidth*0.62, 5, tr(label), "", 0, "L", false, 0, "")

	pdf.SetFont("Helvetica", "B", 9.5)
	pdf.CellFormat(contentWidth*0.38, 5,
		tr(fmt.Sprintf("%.0f / 100   (weight %.2f)", sig.Score, sig.Weight)), "", 1, "R", false, 0, "")

	// The meter. A bar is not decoration here: it lets a reader compare five
	// numbers without arithmetic.
	meterY := pdf.GetY() + 0.5
	pdf.SetFillColor(colPaleSky[0], colPaleSky[1], colPaleSky[2])
	pdf.Rect(marginLeft, meterY, contentWidth, 1.8, "F")
	if sig.Available && sig.Score > 0 {
		pdf.SetFillColor(colSeaGreen[0], colSeaGreen[1], colSeaGreen[2])
		pdf.Rect(marginLeft, meterY, contentWidth*clamp01(sig.Score/100), 1.8, "F")
	}
	pdf.SetY(meterY + 3.4)

	pdf.SetFont("Helvetica", "", 8.5)
	pdf.SetTextColor(colBodyText[0], colBodyText[1], colBodyText[2])
	pdf.MultiCell(contentWidth, 4, tr(sig.Method), "", "L", false)

	if counts := formatRawCounts(sig.RawCounts); counts != "" {
		pdf.SetFont("Helvetica", "I", 8)
		pdf.SetTextColor(colMutedText[0], colMutedText[1], colMutedText[2])
		pdf.MultiCell(contentWidth, 4, tr("Observed: "+counts), "", "L", false)
	}
	pdf.Ln(2)
}

// renderActivityTimeline renders the burst chart, with anomalous bins
// annotated by z-score and the bin width stated.
func renderActivityTimeline(pdf *fpdf.Fpdf, d Data) {
	sectionHeading(pdf, "3. Activity timeline")

	if len(d.Timeline.Bins) == 0 {
		body(pdf, "No timeline bins were captured for this detection.")
		return
	}

	body(pdf, fmt.Sprintf(
		"Posting volume across the detection window, in %s bins. Bars marked in gold exceeded the group's own "+
			"baseline by more than three standard deviations; their z-scores are listed below the chart.",
		durationLabel(d.Timeline.BinWidthSeconds)))
	pdf.Ln(2)

	const chartHeight = 42.0
	top := pdf.GetY()

	peak := 0
	for _, b := range d.Timeline.Bins {
		if b.PostCount > peak {
			peak = b.PostCount
		}
	}
	if peak == 0 {
		peak = 1
	}

	pdf.SetFillColor(colMintCream[0], colMintCream[1], colMintCream[2])
	pdf.Rect(marginLeft, top, contentWidth, chartHeight, "F")

	barWidth := contentWidth / float64(len(d.Timeline.Bins))
	for i, b := range d.Timeline.Bins {
		h := chartHeight * float64(b.PostCount) / float64(peak)
		if h < 0.3 && b.PostCount > 0 {
			h = 0.3
		}
		if b.IsAnomalous {
			pdf.SetFillColor(colGold[0], colGold[1], colGold[2])
		} else {
			pdf.SetFillColor(colGlaucous[0], colGlaucous[1], colGlaucous[2])
		}
		pdf.Rect(marginLeft+float64(i)*barWidth, top+chartHeight-h, barWidth*0.85, h, "F")
	}

	pdf.SetDrawColor(colPaleSky[0], colPaleSky[1], colPaleSky[2])
	pdf.Line(marginLeft, top+chartHeight, pageWidth-marginRight, top+chartHeight)
	pdf.SetY(top + chartHeight + 1.5)

	pdf.SetFont("Helvetica", "", 7.5)
	pdf.SetTextColor(colMutedText[0], colMutedText[1], colMutedText[2])
	pdf.CellFormat(contentWidth/2, 4, tr(d.Timeline.WindowStart.UTC().Format("2 Jan 15:04 MST")), "", 0, "L", false, 0, "")
	pdf.CellFormat(contentWidth/2, 4, tr(d.Timeline.WindowEnd.UTC().Format("2 Jan 15:04 MST")), "", 1, "R", false, 0, "")
	pdf.Ln(2)

	keyValue(pdf, "Bin width", durationLabel(d.Timeline.BinWidthSeconds))
	keyValue(pdf, "Bins", strconv.Itoa(len(d.Timeline.Bins)))
	keyValue(pdf, "Anomalous bins", strconv.Itoa(d.Timeline.AnomalousCount))

	anomalies := make([]dto.BurstBin, 0, d.Timeline.AnomalousCount)
	for _, b := range d.Timeline.Bins {
		if b.IsAnomalous {
			anomalies = append(anomalies, b)
		}
	}
	sort.SliceStable(anomalies, func(i, j int) bool { return anomalies[i].ZScore > anomalies[j].ZScore })
	if len(anomalies) > 12 {
		anomalies = anomalies[:12]
	}
	if len(anomalies) > 0 {
		pdf.Ln(1)
		subHeading(pdf, "Most anomalous bins")
		for _, b := range anomalies {
			bullet(pdf, fmt.Sprintf("%s — %d posts, z = %.2f",
				b.BinStart.UTC().Format("2 Jan 15:04:05 MST"), b.PostCount, b.ZScore))
		}
	}
}

// renderNetworkStructure renders the graph figure from the stored layout,
// plus internal density, conductance and the comparison-account count.
//
// Rendered from the snapshot's coordinates, never recomputed. A force-directed
// layout lands somewhere different on every run, so recomputing here would break
// the byte-identical requirement and would also make the report's figure differ
// from the one the analyst looked at when they decided to send it.
func renderNetworkStructure(pdf *fpdf.Fpdf, d Data) {
	sectionHeading(pdf, "4. Network structure")

	body(pdf, "Each dot is an account and each line is a retained behavioural edge between two accounts. "+
		"Dots in grey are accounts that were active on the same claim but did not cluster — they are shown for "+
		"contrast, so the density of the flagged group can be judged against ordinary activity.")
	pdf.Ln(2)

	keyValue(pdf, "Internal density", fmt.Sprintf("%.3f", d.Network.WhyFlagged.InternalDensity))
	keyValue(pdf, "Conductance", fmt.Sprintf("%.3f", d.Network.WhyFlagged.Conductance))
	keyValue(pdf, "Accounts in cluster", strconv.Itoa(d.Graph.MemberCount))
	keyValue(pdf, "Comparison accounts shown", strconv.Itoa(d.Graph.ComparisonCount))
	if d.Graph.Reduced {
		keyValue(pdf, "Figure reduced", d.Graph.ReductionNote)
	}
	pdf.Ln(2)

	drawGraph(pdf, d.Graph)
}

// drawGraph plots the stored ForceAtlas2 coordinates.
func drawGraph(pdf *fpdf.Fpdf, g dto.NetworkGraph) {
	positioned := make(map[string][2]float64, len(g.Nodes))
	minX, minY := 0.0, 0.0
	maxX, maxY := 0.0, 0.0
	first := true

	for _, n := range g.Nodes {
		if n.X == nil || n.Y == nil {
			continue
		}
		positioned[n.AccountID] = [2]float64{*n.X, *n.Y}
		if first {
			minX, maxX, minY, maxY = *n.X, *n.X, *n.Y, *n.Y
			first = false
			continue
		}
		minX = minFloat(minX, *n.X)
		maxX = maxFloat(maxX, *n.X)
		minY = minFloat(minY, *n.Y)
		maxY = maxFloat(maxY, *n.Y)
	}

	if len(positioned) == 0 {
		// The pipeline stores layout coordinates precisely so this does not
		// happen; when it does, saying so is better than printing an empty box
		// that reads as "no network".
		body(pdf, "No stored layout coordinates were available for this detection, so the graph figure "+
			"could not be reproduced. The account annex and the edge decomposition remain complete.")
		return
	}

	const boxHeight = 95.0
	top := pdf.GetY()
	pdf.SetFillColor(colMintCream[0], colMintCream[1], colMintCream[2])
	pdf.Rect(marginLeft, top, contentWidth, boxHeight, "F")

	pad := 8.0
	spanX := maxX - minX
	spanY := maxY - minY
	if spanX == 0 {
		spanX = 1
	}
	if spanY == 0 {
		spanY = 1
	}
	project := func(p [2]float64) (float64, float64) {
		x := marginLeft + pad + (p[0]-minX)/spanX*(contentWidth-2*pad)
		y := top + pad + (p[1]-minY)/spanY*(boxHeight-2*pad)
		return x, y
	}

	pdf.SetDrawColor(colGlaucous[0], colGlaucous[1], colGlaucous[2])
	pdf.SetLineWidth(0.12)
	for _, e := range g.Edges {
		a, okA := positioned[e.Source]
		b, okB := positioned[e.Target]
		if !okA || !okB {
			continue
		}
		x1, y1 := project(a)
		x2, y2 := project(b)
		pdf.Line(x1, y1, x2, y2)
	}

	for _, n := range g.Nodes {
		p, ok := positioned[n.AccountID]
		if !ok {
			continue
		}
		x, y := project(p)
		r := 0.7 + 1.6*clamp01(n.EigenvectorCentrality)
		if n.Role == models.MembershipComparison {
			pdf.SetFillColor(colPaleSky[0], colPaleSky[1], colPaleSky[2])
			r = 0.6
		} else if n.Allowlisted {
			pdf.SetFillColor(colGold[0], colGold[1], colGold[2])
		} else {
			pdf.SetFillColor(colSeaGreen[0], colSeaGreen[1], colSeaGreen[2])
		}
		pdf.Circle(x, y, r, "F")
	}

	pdf.SetY(top + boxHeight + 2)
	pdf.SetFont("Helvetica", "", 7.5)
	pdf.SetTextColor(colMutedText[0], colMutedText[1], colMutedText[2])
	pdf.MultiCell(contentWidth, 4,
		tr("Legend: green — accounts in the detected cluster, sized by how central they are within it; "+
			"grey — accounts active on the same claim that did not cluster; gold — accounts on the "+
			"declared-coordination allowlist. Layout reproduced from the stored detection snapshot."),
		"", "L", false)
	pdf.Ln(1)
}

// renderRepresentativeContent renders the top duplicate groups: canonical
// text, variant count, and a table of variants with handle, timestamp to the
// second, and text, with shared spans highlighted.
func renderRepresentativeContent(pdf *fpdf.Fpdf, d Data) {
	sectionHeading(pdf, "5. Representative content")

	if len(d.Content.Groups) == 0 {
		body(pdf, "No duplicate content groups were captured for this detection.")
		return
	}

	body(pdf, "Posts are reproduced from the evidence snapshot taken at detection time. Content deleted since "+
		"capture is still shown, marked accordingly — deletion after a campaign concludes is expected and does not "+
		"reduce what was observed. Per-post SHA-256 digests are listed in the account annex appendix.")
	pdf.Ln(2)

	groups := d.Content.Groups
	if len(groups) > 8 {
		groups = groups[:8]
	}

	for i, g := range groups {
		if pdf.GetY() > 215 {
			pdf.AddPage()
		}
		subHeading(pdf, fmt.Sprintf("Duplicate group %d — %d variants", i+1, g.VariantCount))

		pdf.SetFont("Helvetica", "I", 8.5)
		pdf.SetTextColor(colBodyText[0], colBodyText[1], colBodyText[2])
		pdf.SetFillColor(colMintCream[0], colMintCream[1], colMintCream[2])
		pdf.MultiCell(contentWidth, 4, tr("Canonical text: "+truncateWords(g.CanonicalText, 70)), "", "L", true)
		pdf.Ln(1)

		variants := g.Variants
		if len(variants) > 10 {
			variants = variants[:10]
		}
		for _, v := range variants {
			pdf.SetFont("Helvetica", "B", 8)
			pdf.SetTextColor(colRegalNavy[0], colRegalNavy[1], colRegalNavy[2])
			marker := ""
			if !v.StillPublic {
				marker = "   [" + detector.AvailabilityDeleted + "]"
			}
			pdf.MultiCell(contentWidth, 4,
				tr(fmt.Sprintf("@%s  ·  %s%s", v.Handle, v.PostedAt.UTC().Format("2006-01-02 15:04:05 MST"), marker)),
				"", "L", false)

			pdf.SetFont("Helvetica", "", 8)
			pdf.SetTextColor(colBodyText[0], colBodyText[1], colBodyText[2])
			pdf.MultiCell(contentWidth, 4, tr(markSharedSpan(v)), "", "L", false)
			pdf.Ln(1)
		}
		if len(g.Variants) > len(variants) {
			mutedNote(pdf, fmt.Sprintf("%d further variants in this group are listed in the evidence bundle.",
				len(g.Variants)-len(variants)))
		}
		pdf.Ln(2)
	}
	if len(d.Content.Groups) > len(groups) {
		mutedNote(pdf, fmt.Sprintf("%d further duplicate groups are included in the machine-readable evidence bundle.",
			len(d.Content.Groups)-len(groups)))
	}
}

// markSharedSpan brackets the span a variant shares with its group's canonical
// text.
//
// The shared span is highlighted so a reader can see WHICH part of a variant
// is common to the group. PDF has no <mark>, so the highlight is rendered as
// bracketing instead — bracketing carries that in plain text, in the CSV, and
// read aloud.
func markSharedSpan(p dto.EvidencePost) string {
	text := p.Text
	if p.SharedSpanStart == nil || p.SharedSpanEnd == nil {
		return truncateWords(text, 60)
	}
	runes := []rune(text)
	start, end := *p.SharedSpanStart, *p.SharedSpanEnd
	if start < 0 || end > len(runes) || start >= end {
		return truncateWords(text, 60)
	}
	marked := string(runes[:start]) + "[[" + string(runes[start:end]) + "]]" + string(runes[end:])
	return truncateWords(marked, 60)
}

// renderAccountAnnex renders the full member table, one row per account.
//
// Mandatory in a platform referral and non-toggleable: a referral without the
// account list is not actionable.
func renderAccountAnnex(pdf *fpdf.Fpdf, d Data) {
	pdf.AddPage()
	sectionHeading(pdf, "6. Account annex")

	body(pdf, "Every account in the detected cluster, with the behaviour measured for it. These are observations "+
		"about posting patterns. No column asserts that an account is automated or operated in bad faith.")
	pdf.Ln(2)

	headers := []string{"Handle", "Platform", "Created", "Posts", "Dup.", "Interval", "Clock", "Centrality"}
	widths := []float64{44, 22, 22, 14, 14, 20, 16, 22}

	drawHeader := func() {
		pdf.SetFont("Helvetica", "B", 7.5)
		pdf.SetFillColor(colRegalNavy[0], colRegalNavy[1], colRegalNavy[2])
		pdf.SetTextColor(255, 255, 255)
		for i, h := range headers {
			pdf.CellFormat(widths[i], 5, tr(h), "", 0, "L", true, 0, "")
		}
		pdf.Ln(-1)
	}
	drawHeader()

	pdf.SetFont("Helvetica", "", 7.5)
	pdf.SetTextColor(colBodyText[0], colBodyText[1], colBodyText[2])
	for i, a := range d.Accounts {
		if pdf.GetY() > 262 {
			pdf.AddPage()
			drawHeader()
			pdf.SetFont("Helvetica", "", 7.5)
			pdf.SetTextColor(colBodyText[0], colBodyText[1], colBodyText[2])
		}

		fill := i%2 == 1
		if fill {
			pdf.SetFillColor(colMintCream[0], colMintCream[1], colMintCream[2])
		}

		created := "unknown"
		if a.CreatedAtPlatform != nil {
			created = a.CreatedAtPlatform.UTC().Format("2006-01-02")
		}
		interval := "n/a"
		if a.MedianInterpostSecs != nil {
			interval = durationLabel(int(*a.MedianInterpostSecs))
		}
		handle := "@" + a.Handle
		if a.Allowlisted {
			handle += " *"
		}

		cells := []string{
			truncateChars(handle, 26),
			truncateChars(a.Platform, 12),
			created,
			strconv.Itoa(a.PostsInCluster),
			fmt.Sprintf("%.2f", a.DuplicationRate),
			interval,
			fmt.Sprintf("%.0f%%", a.CircadianCoverage*100),
			fmt.Sprintf("%.3f", a.EigenvectorCentrality),
		}
		for j, c := range cells {
			pdf.CellFormat(widths[j], 4.6, tr(c), "", 0, "L", fill, 0, "")
		}
		pdf.Ln(-1)
	}

	pdf.Ln(2)
	mutedNote(pdf, "Columns: Dup. — share of this account's posts that duplicate another member's; Interval — "+
		"median gap between its posts; Clock — share of the 24 hourly buckets in which it was active; Centrality — "+
		"eigenvector centrality within the cluster. An asterisk marks an account on the declared-coordination "+
		"allowlist.")
}

// renderInternalContext renders internal-briefing-only material: analyst
// notes, review history, and the linked claim and policy context.
//
// Never rendered in a platform referral, so one detection can serve both
// audiences without the referral carrying the city's internal deliberation
// into a third party's hands.
func renderInternalContext(pdf *fpdf.Fpdf, d Data) {
	sectionHeading(pdf, "7. Internal context")
	mutedNote(pdf, "This section appears in internal briefings only and is omitted from platform referrals.")
	pdf.Ln(1)

	subHeading(pdf, "Linked claims")
	if len(d.Network.LinkedClaims) == 0 {
		body(pdf, "No claims are linked to this network.")
	}
	for _, c := range d.Network.LinkedClaims {
		role := "secondary"
		if c.IsPrimary {
			role = "primary"
		}
		bullet(pdf, fmt.Sprintf("%s (%s, overlap %.3f)", truncateWords(c.ClaimStatement, 24), role, c.OverlapRatio))
	}

	pdf.Ln(2)
	subHeading(pdf, "Linked public policies")
	if len(d.Network.LinkedPolicies) == 0 {
		body(pdf, "No public policies are correlated with the linked claims.")
	}
	for _, p := range d.Network.LinkedPolicies {
		status := ""
		if p.Status != nil {
			status = " — " + strings.ReplaceAll(*p.Status, "_", " ")
		}
		bullet(pdf, p.Name+status)
	}

	pdf.Ln(2)
	subHeading(pdf, "Review history")
	if len(d.ReviewLog) == 0 {
		body(pdf, "No review decisions have been recorded for this network.")
	}
	for _, entry := range d.ReviewLog {
		who := "unattributed"
		if entry.UserID != nil {
			who = *entry.UserID
		}
		if d.RedactAnalystNames {
			who = "[redacted]"
		}
		bullet(pdf, fmt.Sprintf("%s — %s to %s by %s",
			entry.CreatedAt.UTC().Format("2006-01-02 15:04 MST"),
			statusLabel(entry.FromStatus), statusLabel(entry.ToStatus), who))
		pdf.SetFont("Helvetica", "I", 8)
		pdf.SetTextColor(colMutedText[0], colMutedText[1], colMutedText[2])
		pdf.MultiCell(contentWidth-6, 4, tr("      "+truncateWords(entry.Reason, 50)), "", "L", false)
	}
}

// renderMethodology renders every parameter value used, model and library
// versions, the random seed, the null-model settings, normalisation rules,
// and all exclusions applied — the section that makes the report reproducible.
func renderMethodology(pdf *fpdf.Fpdf, d Data) {
	pdf.AddPage()
	sectionHeading(pdf, "Appendix A — Methodology")

	body(pdf, "The detection that produced this report ran with the configuration below. Parameters are recorded "+
		"per run, so a later change to the system's settings does not alter what this document describes.")
	pdf.Ln(2)

	subHeading(pdf, "Detection run")
	keyValue(pdf, "Run ID", d.RunID)
	keyValue(pdf, "Trigger", d.Network.Run.TriggerSource)
	keyValue(pdf, "Window", fmt.Sprintf("%s to %s",
		d.Network.Run.WindowStart.UTC().Format("2006-01-02 15:04 MST"),
		d.Network.Run.WindowEnd.UTC().Format("2006-01-02 15:04 MST")))
	keyValue(pdf, "Candidate accounts", strconv.Itoa(d.Network.Run.CandidatesCount))
	keyValue(pdf, "Candidate set truncated", yesNo(d.Network.Run.Truncated))

	if len(d.RunParameters) > 0 {
		pdf.Ln(2)
		subHeading(pdf, "Parameters in force")
		keys := make([]string, 0, len(d.RunParameters))
		for k := range d.RunParameters {
			keys = append(keys, k)
		}
		// Sorted so two renders of the same run emit the same bytes: Go map
		// iteration order is randomised, and this appendix is the one place a
		// map is walked.
		sort.Strings(keys)
		for _, k := range keys {
			keyValue(pdf, humanKey(k), fmt.Sprintf("%v", d.RunParameters[k]))
		}
	}

	pdf.Ln(2)
	subHeading(pdf, "Exclusions applied")
	for _, e := range []string{
		"Accounts on the declared-coordination allowlist were removed before the graph was built.",
		"The city's own communications accounts were removed by self-exclusion.",
		"Platform-native reshares of the same parent post were excluded from the duplication measure; identical text there is a platform artefact, not authored duplication.",
		"Posts shorter than the configured minimum length were excluded; very short posts are identical across thousands of unrelated people.",
		"Text on the common-phrase allowlist — official slogans, hashtags, standard policy names, quoted press-release lines — was excluded; residents quoting the same government announcement are not colluding.",
	} {
		bullet(pdf, e)
	}

	pdf.Ln(2)
	subHeading(pdf, "Known limitations of the method")
	for _, l := range detector.KnownLimitations {
		bullet(pdf, l)
	}
}

// renderLimitations renders the full standing disclaimer and limitations text.
//
// The disclaimer appears twice in this document by design — here and on the
// cover. That is not an oversight to be tidied up.
func renderLimitations(pdf *fpdf.Fpdf, d Data) {
	sectionHeading(pdf, "Appendix B — Limitations and disclaimer")
	calloutBox(pdf, colSeaGreen, "Standing disclaimer", detector.Disclaimer)
	pdf.Ln(2)

	body(pdf, "This system does not infer, assert, or estimate who operates an account, who funds a network, or any "+
		"real-world identity, and it never labels an individual account as automated. Its claim is only that a set of "+
		"accounts exhibited measurable coordinated behaviour within the window stated above. No enforcement action is "+
		"taken automatically: every outward step, including the generation of this document, is a human decision with "+
		"a recorded justification.")
}

// renderChainOfCustody renders the evidence snapshot ID, snapshot hash,
// detection run ID, and the export audit entry ID.
//
// All four. The audit entry id is why the audit row is created before rendering
// rather than after: it has to exist to be printed here, and a chain of custody
// missing a link is what separates a document from an assertion.
func renderChainOfCustody(pdf *fpdf.Fpdf, d Data) {
	sectionHeading(pdf, "Appendix C — Chain of custody")

	body(pdf, "These identifiers tie this document to the immutable evidence snapshot it was built from. The "+
		"snapshot digest covers the captured posts, account metadata and computed metrics as they existed at "+
		"detection time.")
	pdf.Ln(2)

	keyValue(pdf, "Report ID", d.ReportID)
	keyValue(pdf, "Evidence snapshot ID", orDash(d.SnapshotID))
	keyValue(pdf, "Snapshot SHA-256", orDash(d.SnapshotSHA256))
	keyValue(pdf, "Detection run ID", d.RunID)
	keyValue(pdf, "Export audit entry ID", orDash(d.AuditID))
	keyValue(pdf, "Report type", reportTypeLabel(d.Type))
	keyValue(pdf, "Sections included", sectionsLabel(d.Sections))
	keyValue(pdf, "Analyst names redacted", yesNo(d.RedactAnalystNames))
}

// --- layout helpers ---

func sectionHeading(pdf *fpdf.Fpdf, title string) {
	if pdf.GetY() > 245 {
		pdf.AddPage()
	} else if pdf.PageNo() == 0 {
		pdf.AddPage()
	}
	pdf.Ln(4)
	pdf.SetFont("Helvetica", "B", 13)
	pdf.SetTextColor(colRegalNavy[0], colRegalNavy[1], colRegalNavy[2])
	pdf.CellFormat(contentWidth, 7, tr(title), "", 1, "L", false, 0, "")

	pdf.SetDrawColor(colSeaGreen[0], colSeaGreen[1], colSeaGreen[2])
	pdf.SetLineWidth(0.6)
	y := pdf.GetY()
	pdf.Line(marginLeft, y, marginLeft+30, y)
	pdf.Ln(3)
}

func subHeading(pdf *fpdf.Fpdf, title string) {
	if pdf.GetY() > 258 {
		pdf.AddPage()
	}
	pdf.SetFont("Helvetica", "B", 10)
	pdf.SetTextColor(colRegalNavy[0], colRegalNavy[1], colRegalNavy[2])
	pdf.CellFormat(contentWidth, 5.5, tr(title), "", 1, "L", false, 0, "")
	pdf.Ln(0.5)
}

func body(pdf *fpdf.Fpdf, text string) {
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(colBodyText[0], colBodyText[1], colBodyText[2])
	pdf.MultiCell(contentWidth, 4.6, tr(text), "", "L", false)
}

func mutedNote(pdf *fpdf.Fpdf, text string) {
	pdf.SetFont("Helvetica", "I", 7.5)
	pdf.SetTextColor(colMutedText[0], colMutedText[1], colMutedText[2])
	pdf.MultiCell(contentWidth, 4, tr(text), "", "L", false)
}

func bullet(pdf *fpdf.Fpdf, text string) {
	if pdf.GetY() > 262 {
		pdf.AddPage()
	}
	pdf.SetFont("Helvetica", "", 8.5)
	pdf.SetTextColor(colBodyText[0], colBodyText[1], colBodyText[2])
	pdf.CellFormat(4, 4.4, tr("-"), "", 0, "L", false, 0, "")
	pdf.MultiCell(contentWidth-4, 4.4, tr(text), "", "L", false)
}

func keyValue(pdf *fpdf.Fpdf, key, value string) {
	if pdf.GetY() > 262 {
		pdf.AddPage()
	}
	pdf.SetFont("Helvetica", "B", 8.5)
	pdf.SetTextColor(colMutedText[0], colMutedText[1], colMutedText[2])
	pdf.CellFormat(52, 5, tr(key), "", 0, "L", false, 0, "")

	pdf.SetFont("Helvetica", "", 8.5)
	pdf.SetTextColor(colBodyText[0], colBodyText[1], colBodyText[2])
	pdf.MultiCell(contentWidth-52, 5, tr(value), "", "L", false)
}

func calloutBox(pdf *fpdf.Fpdf, accent [3]int, title, text string) {
	pdf.SetFont("Helvetica", "", 8.5)
	lines := pdf.SplitLines([]byte(tr(text)), contentWidth-10)
	height := 9 + float64(len(lines))*4.4

	if pdf.GetY()+height > 268 {
		pdf.AddPage()
	}

	top := pdf.GetY()
	pdf.SetFillColor(colMintCream[0], colMintCream[1], colMintCream[2])
	pdf.Rect(marginLeft, top, contentWidth, height, "F")
	pdf.SetFillColor(accent[0], accent[1], accent[2])
	pdf.Rect(marginLeft, top, 1.6, height, "F")

	pdf.SetXY(marginLeft+5, top+2)
	pdf.SetFont("Helvetica", "B", 8.5)
	pdf.SetTextColor(colRegalNavy[0], colRegalNavy[1], colRegalNavy[2])
	pdf.CellFormat(contentWidth-10, 4.5, tr(title), "", 1, "L", false, 0, "")

	pdf.SetX(marginLeft + 5)
	pdf.SetFont("Helvetica", "", 8.5)
	pdf.SetTextColor(colBodyText[0], colBodyText[1], colBodyText[2])
	pdf.MultiCell(contentWidth-10, 4.4, tr(text), "", "L", false)
	pdf.SetY(top + height + 1)
}

// tr maps the text onto the Latin-1 range the core PDF fonts cover.
//
// Core fonts are used deliberately — embedding a font file would introduce a
// subsetting step that varies between runs and break the byte-identical
// requirement. The cost is that characters outside Latin-1 have to be folded,
// which for the Bahasa Indonesia and English content in scope means typographic
// punctuation rather than letters.
func tr(s string) string {
	replacer := strings.NewReplacer(
		"—", "-", "–", "-",
		"‘", "'", "’", "'",
		"“", "\"", "”", "\"",
		"…", "...", " ", " ",
		"•", "-", "→", "->",
	)
	s = replacer.Replace(s)

	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(' ')
		case r < 32:
			// Control characters would corrupt the content stream.
		case r <= 0xFF:
			b.WriteRune(r)
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

func formatRawCounts(raw any) string {
	if raw == nil {
		return ""
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return fmt.Sprintf("%v", raw)
	}

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %v", humanKey(k), m[k]))
	}
	return strings.Join(parts, ", ")
}

func humanKey(k string) string {
	return strings.ReplaceAll(k, "_", " ")
}

func reportTypeLabel(t string) string {
	if t == models.ReportTypeInternalBriefing {
		return "Internal briefing"
	}
	return "Platform referral"
}

func bandLabel(band string) string {
	switch band {
	case models.ConfidenceHigh:
		return "High"
	case models.ConfidenceMedium:
		return "Medium"
	default:
		return "Low"
	}
}

func statusLabel(status string) string {
	switch status {
	case models.NetworkStatusUnderReview:
		return "Under Review"
	case models.NetworkStatusConfirmed:
		return "Confirmed"
	case models.NetworkStatusDismissedFP:
		return "Dismissed - False Positive"
	case models.NetworkStatusActionTaken:
		return "Action Taken"
	default:
		return "Unreviewed"
	}
}

func sectionsLabel(s dto.ReportSections) string {
	var on []string
	if s.Graph {
		on = append(on, "graph figure")
	}
	if s.ContentClusters {
		on = append(on, "content clusters")
	}
	if s.AccountAnnex {
		on = append(on, "account annex")
	}
	if s.Methodology {
		on = append(on, "methodology appendix")
	}
	if len(on) == 0 {
		return "core sections only"
	}
	return strings.Join(on, ", ")
}

func strongestSignal(signals []dto.SignalDetail) *dto.SignalDetail {
	var best *dto.SignalDetail
	for i := range signals {
		if !signals[i].Available {
			continue
		}
		if best == nil || signals[i].Score > best.Score {
			best = &signals[i]
		}
	}
	return best
}

func firstSeenLabel(r dto.RecurrenceInfo) string {
	if r.FirstSeenAt == nil {
		return "its first detection"
	}
	return r.FirstSeenAt.UTC().Format("2 January 2006")
}

func durationLabel(seconds int) string {
	switch {
	case seconds <= 0:
		return "n/a"
	case seconds < 60:
		return fmt.Sprintf("%d s", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%.0f min", float64(seconds)/60)
	case seconds < 86400:
		return fmt.Sprintf("%.1f h", float64(seconds)/3600)
	default:
		return fmt.Sprintf("%.1f d", float64(seconds)/86400)
	}
}

func truncateWords(s string, limit int) string {
	fields := strings.Fields(s)
	if len(fields) <= limit {
		return strings.Join(fields, " ")
	}
	return strings.Join(fields[:limit], " ") + "..."
}

// capWords enforces the 200-word ceiling on the executive summary.
func capWords(s string, limit int) string {
	fields := strings.Fields(s)
	if len(fields) <= limit {
		return strings.Join(fields, " ")
	}
	return strings.Join(fields[:limit], " ") + "..."
}

func truncateChars(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "."
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// ensure time is referenced even if a future edit removes its only use.
var _ = time.Time{}
