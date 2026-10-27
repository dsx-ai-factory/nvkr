// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runpresenter

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/dsx-ai-factory/nvkr/internal/tuitheme"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Action affects only session navigation or its already-owned cancellation.
type Action uint8

const (
	None Action = iota
	Close
	Cancel
)

type selection struct{ output, operation string }

const maxBackHistory = 64

// View is confined to the terminal loop. The source owns concurrent snapshots;
// no widget, callback or terminal queue is exposed to producers.
type View struct {
	live, noColor                                               bool
	snapshot                                                    progress.Snapshot
	visible                                                     []progress.Output
	selected, operation                                         string
	back                                                        []selection
	focus, filter, detailOffset, linkIndex, rows, activityStart int
	query                                                       string
	searching, help, follow                                     bool
	anchor                                                      uint64
	links                                                       []string
	helpOffset                                                  int
	operations                                                  map[string]progress.Operation
	confirmQuit                                                 bool
	commandItems                                                map[string]progress.Item
	primaryCount                                                int
	showAll                                                     bool
	browseBlocked                                               bool
}

func NewView(live, noColor bool) *View {
	return &View{live: live, noColor: noColor, follow: true, rows: 8}
}

// safeText is terminal safety only. The owning progress projection is responsible
// for excluding private/provider/raw diagnostic content before this boundary.
func safeText(value string) string {
	var text strings.Builder
	for _, r := range strings.ToValidUTF8(value, "�") {
		if text.Len() >= 4096 {
			text.WriteString("…")
			break
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			quoted := strconv.QuoteRuneToASCII(r)
			text.WriteString(quoted[1 : len(quoted)-1])
		} else {
			text.WriteRune(r)
		}
	}
	return text.String()
}

func (v *View) update(snapshot progress.Snapshot) {
	v.snapshot = snapshot
	if v.focus == 2 && !v.hasActivity() {
		v.focus = 0
	}
	if v.commandMode() && !slices.Contains(v.availableFilters(), v.filter) {
		v.filter = 0
	}
	v.operations = make(map[string]progress.Operation, len(snapshot.Operations))
	for _, operation := range snapshot.Operations {
		v.operations[operation.ID] = operation
	}
	v.visible = nil
	v.primaryCount = 0
	if v.commandMode() {
		v.commandItems = make(map[string]progress.Item, len(snapshot.Items))
		for _, item := range snapshot.Items {
			if item.Primary {
				v.primaryCount++
			}
			v.commandItems[item.ID] = item
		}
	}
	query := strings.ToLower(v.query)
	appendVisible := func(output progress.Output) {
		_, _, failed := v.outputSummary(output)
		active := output.State != "observed" && !failed
		matches := query == "" || strings.Contains(strings.ToLower(output.Name), query)
		if v.commandMode() {
			if v.overview() && !v.commandItems[output.Name].Primary {
				return
			}
			matches = commandMatches(v.commandItems[output.Name], query)
			active = output.State == "changed" || output.State == "added" || output.State == "removed"
		}
		if v.filter == 1 && !active || v.filter == 2 && !failed || !matches {
			return
		}
		v.visible = append(v.visible, output)
	}
	if v.commandMode() {
		// Output-shaped rows keep navigation shared without allocating a
		// second table. They describe command items, never Run membership.
		for _, item := range snapshot.Items {
			appendVisible(progress.Output{Name: item.ID, Kind: item.Kind, State: item.State, Stage: item.Name, Dependencies: item.Dependencies})
		}
	} else {
		for _, output := range snapshot.Outputs {
			appendVisible(output)
		}
	}
	slices.SortFunc(v.visible, func(a, b progress.Output) int {
		if v.commandMode() {
			left, right := a.Stage, b.Stage
			if left == "" {
				left = a.Name
			}
			if right == "" {
				right = b.Name
			}
			if order := strings.Compare(left, right); order != 0 {
				return order
			}
		}
		return strings.Compare(a.Name, b.Name)
	})
	if v.selected == "" && len(v.visible) > 0 {
		v.selected = v.visible[0].Name
	}
}

// outputSummary joins exact operation references for descriptive display only.
// A finished producer never increments lifecycle's observed membership count.
func (v *View) outputSummary(output progress.Output) (state, stage string, failed bool) {
	if v.commandMode() {
		return output.State, output.Kind, output.State == "failed"
	}
	if output.State == "observed" {
		return "Observed", output.Stage, false
	}
	if output.State == "failed" {
		return "Output failed", output.Stage, true
	}
	state, stage = "Waiting", output.Stage
	completed := 0
	canceledStage := ""
	for _, id := range output.Operations {
		if operation, ok := v.operations[id]; ok {
			switch operation.State {
			case "failed":
				return "Producer failed", operation.Kind + " " + operation.ID, true
			case "running":
				state, stage = "Building", operation.Kind+" "+operation.ID
			case "completed", "cached", "skipped":
				completed++
			case "canceled":
				canceledStage = operation.ID
				if state != "Building" {
					state, stage = "Producer canceled", operation.ID
				}
			}
		}
	}
	if v.snapshot.Status == "failed" && output.State == "required" {
		if canceledStage != "" {
			return "Producer canceled", canceledStage, false
		}
		return "Not retained", "No observation in final Run record", false
	}
	if output.State == "observing" {
		return "Observing", output.Stage, false
	}
	if len(output.Operations) > 0 && completed == len(output.Operations) {
		return "Check pending", "Producer finished", false
	}
	return state, stage, false
}

func (v *View) selectedOutput() (progress.Output, bool) {
	for _, output := range v.snapshot.Outputs {
		if output.Name == v.selected {
			return output, true
		}
	}
	return progress.Output{}, false
}

// A user-applied filter/search may deliberately select a visible row. Snapshot
// updates never call this: telemetry cannot steal a previously pinned identity.
func (v *View) applyInputSelection() {
	v.update(v.snapshot)
	if slices.ContainsFunc(v.visible, func(o progress.Output) bool { return o.Name == v.selected }) {
		return
	}
	v.selected, v.operation = "", ""
	if len(v.visible) > 0 {
		v.selected = v.visible[0].Name
	}
	v.detailOffset, v.linkIndex = 0, 0
	v.back = nil
}

func (v *View) rememberSelection() {
	if len(v.back) == maxBackHistory {
		copy(v.back, v.back[1:])
		v.back = v.back[:maxBackHistory-1]
	}
	v.back = append(v.back, selection{v.selected, v.operation})
}

func (v *View) dismissConfirmation() { v.confirmQuit = false }

// Input routes keys through the focused pane. Printable search input takes
// precedence over q, f and other navigation shortcuts. Ctrl-C cancels live work
// and closes a read-only view without changing its completed owner outcome.
func (v *View) Input(event *tcell.EventKey) Action {
	if event.Key() == tcell.KeyCtrlC {
		v.dismissConfirmation()
		if !v.live {
			return Close
		}
		return Cancel
	}
	if v.confirmQuit {
		switch {
		case event.Key() == tcell.KeyRune && (event.Rune() == 'y' || event.Rune() == 'Y'):
			v.dismissConfirmation()
			if v.live {
				return Cancel
			}
			return Close
		case event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyEnter || event.Key() == tcell.KeyRune && (event.Rune() == 'n' || event.Rune() == 'N'):
			v.dismissConfirmation()
		}
		return None
	}
	if v.browseBlocked {
		if event.Key() == tcell.KeyRune && (event.Rune() == 'q' || event.Rune() == 'Q') {
			v.confirmQuit = true
		}
		return None
	}
	if v.searching {
		switch event.Key() {
		case tcell.KeyEscape:
			v.searching = false
		case tcell.KeyEnter:
			v.searching = false
			v.applyInputSelection()
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			r := []rune(v.query)
			if len(r) > 0 {
				v.query = string(r[:len(r)-1])
			}
		case tcell.KeyRune:
			if !unicode.IsControl(event.Rune()) && !unicode.Is(unicode.Cf, event.Rune()) && len([]rune(v.query)) < 256 {
				v.query += string(event.Rune())
			}
		}
		return None
	}
	if event.Key() == tcell.KeyRune && (event.Rune() == 'q' || event.Rune() == 'Q') {
		v.confirmQuit = true
		return None
	}
	if v.help {
		if event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyRune && event.Rune() == '?' {
			v.help = false
		}
		switch event.Key() {
		case tcell.KeyUp:
			v.helpOffset = max(0, v.helpOffset-1)
		case tcell.KeyDown:
			v.helpOffset++
		case tcell.KeyPgUp:
			v.helpOffset = max(0, v.helpOffset-v.rows)
		case tcell.KeyPgDn:
			v.helpOffset += v.rows
		case tcell.KeyHome:
			v.helpOffset = 0
		}
		return None
	}
	page := max(1, v.rows)
	switch event.Key() {
	case tcell.KeyTab:
		v.focus = (v.focus + 1) % v.tabCount()
	case tcell.KeyBacktab:
		v.focus = (v.focus + v.tabCount() - 1) % v.tabCount()
	case tcell.KeyEscape:
		if len(v.back) > 0 {
			old := v.back[len(v.back)-1]
			v.back = v.back[:len(v.back)-1]
			v.selected, v.operation = old.output, old.operation
			v.detailOffset, v.linkIndex = 0, 0
		} else if v.focus != 0 {
			v.focus = 0
			v.operation = ""
			v.detailOffset, v.linkIndex = 0, 0
		} else {
			v.query = ""
			v.filter = 0
			v.applyInputSelection()
		}
	case tcell.KeyUp, tcell.KeyDown, tcell.KeyPgUp, tcell.KeyPgDn, tcell.KeyHome, tcell.KeyEnd:
		delta := 1
		if event.Key() == tcell.KeyUp || event.Key() == tcell.KeyPgUp {
			delta = -1
		}
		if event.Key() == tcell.KeyPgUp || event.Key() == tcell.KeyPgDn {
			delta *= page
		}
		switch v.focus {
		case 0:
			index := slices.IndexFunc(v.visible, func(o progress.Output) bool { return o.Name == v.selected })
			if index < 0 {
				index = 0
				delta = 0
			}
			if event.Key() == tcell.KeyHome {
				index = 0
				delta = 0
			}
			if event.Key() == tcell.KeyEnd {
				index = len(v.visible) - 1
				delta = 0
			}
			if len(v.visible) > 0 {
				v.selected = v.visible[min(len(v.visible)-1, max(0, index+delta))].Name
				v.operation = ""
				v.detailOffset, v.linkIndex = 0, 0
				v.back = nil
			}
		case 1:
			v.detailOffset = max(0, v.detailOffset+delta)
			if len(v.links) > 0 {
				v.linkIndex = min(len(v.links)-1, max(0, v.linkIndex+delta))
			}
			if event.Key() == tcell.KeyHome {
				v.detailOffset, v.linkIndex = 0, 0
			}
			if event.Key() == tcell.KeyEnd {
				v.detailOffset = 1 << 20
				v.linkIndex = max(0, len(v.links)-1)
			}
		case 2:
			v.follow = false
			v.activityStart = max(0, min(max(0, len(v.snapshot.Activity)-1), v.activityStart+delta))
			if event.Key() == tcell.KeyHome {
				v.activityStart = 0
			}
			if event.Key() == tcell.KeyEnd {
				v.follow = true
			}
			if len(v.snapshot.Activity) > 0 {
				v.anchor = v.snapshot.Activity[v.activityStart].Sequence
			}
		}
	case tcell.KeyEnter:
		if v.focus == 0 {
			v.focus = 1
			v.detailOffset, v.linkIndex = 0, 0
		} else if v.focus == 1 && len(v.links) > 0 {
			ref := v.links[min(v.linkIndex, len(v.links)-1)]
			if v.commandMode() {
				if item, ok := v.commandItems[ref]; ok {
					v.rememberSelection()
					v.selected, v.operation = item.ID, ""
					v.detailOffset, v.linkIndex = 0, 0
				}
				return None
			}
			for _, o := range v.snapshot.Outputs {
				if o.Name == ref {
					v.rememberSelection()
					v.selected, v.operation = o.Name, ""
					v.detailOffset, v.linkIndex = 0, 0
					return None
				}
			}
			for _, o := range v.snapshot.Operations {
				if o.ID == ref {
					v.rememberSelection()
					v.operation = o.ID
					v.detailOffset, v.linkIndex = 0, 0
					return None
				}
			}
		}
	case tcell.KeyRune:
		switch event.Rune() {
		case '/':
			v.searching = true
		case 'f':
			filters := v.availableFilters()
			index := slices.Index(filters, v.filter)
			v.filter = filters[(index+1)%len(filters)]
			v.applyInputSelection()
		case 'g':
			if v.hasOverview() {
				if v.overview() {
					v.showAll = true
				} else {
					v.showAll, v.query, v.filter = false, "", 0
				}
				v.applyInputSelection()
			}
		case 'a':
			if v.hasActivity() {
				v.focus = 2
			}
		case '1', '2', '3':
			focus := int(event.Rune() - '1')
			if focus < v.tabCount() {
				v.focus = focus
			}
		case '?':
			v.help = true
			v.helpOffset = 0
		}
	}
	return None
}

func age(now, when time.Time) string {
	if when.IsZero() {
		return "unavailable"
	}
	return max(time.Duration(0), now.Sub(when)).Truncate(time.Second).String() + " ago"
}
func (v *View) stateLabel() string {
	if v.snapshot.Status == "" {
		return "unavailable"
	}
	return v.snapshot.Status
}

func (v *View) modeLabel() string {
	if v.snapshot.Mode != "" {
		return v.snapshot.Mode
	}
	if v.live {
		return "live"
	}
	return "inspection"
}

func (v *View) clampLinkIndex() {
	v.linkIndex = min(max(0, v.linkIndex), max(0, len(v.links)-1))
}

func (v *View) header(now time.Time, canceling bool) []string {
	if v.commandMode() {
		return v.commandHeader()
	}
	observed := 0
	for _, o := range v.snapshot.Outputs {
		if o.State == "observed" {
			observed++
		}
	}
	mode := v.modeLabel()
	state := v.stateLabel()
	if canceling {
		state = "Canceling; waiting for owned work"
	}
	status := fmt.Sprintf("%s  Outputs observed %d/%d", state, observed, len(v.snapshot.Outputs))
	if v.snapshot.Status == "unavailable" {
		status = state + "; output membership unavailable"
		if v.live && len(v.snapshot.Outputs) > 0 {
			status = fmt.Sprintf("%s; Required %d; observed membership unavailable", state, len(v.snapshot.Outputs))
		}
	}
	first := "NVKR " + safeText(mode) + "  " + safeText(v.snapshot.Attempt)
	if v.live && !v.snapshot.StartedAt.IsZero() {
		first += "  elapsed " + max(time.Duration(0), now.Sub(v.snapshot.StartedAt)).Truncate(time.Second).String()
	}
	lane := "Lane: " + safeText(v.snapshot.Lane) + "; release verification not performed"
	if mode == "recorded" {
		lane = "Recorded observations; current payload availability not checked"
	}
	if v.snapshot.CleanupIncomplete {
		status += "; cleanup incomplete"
	}
	if v.snapshot.Phase != "" {
		status += " | " + safeText(v.snapshot.Phase)
	}
	if v.snapshot.FailureCode != "" {
		lane = "Code: " + safeText(v.snapshot.FailureCode)
	}
	return []string{first, status, lane}
}

func (v *View) outputLines(width int) []string {
	if v.commandMode() {
		return v.commandResults(width)
	}
	filter := []string{"All", "Active", "Failed"}[v.filter]
	lines := []string{fmt.Sprintf("Outputs %d/%d shown; filter %s", len(v.visible), len(v.snapshot.Outputs), filter)}
	if v.query != "" {
		lines = append(lines, "Search: "+safeText(v.query))
	}
	if len(v.visible) == 0 {
		if v.query != "" || v.filter != 0 {
			return append(lines, "No outputs match; Esc clears filter/search")
		}
		return append(lines, "No outputs supplied")
	}
	index := slices.IndexFunc(v.visible, func(o progress.Output) bool { return o.Name == v.selected })
	start := max(0, min(max(0, index-v.rows/2), len(v.visible)-v.rows))
	for _, o := range v.visible[start:min(len(v.visible), start+v.rows)] {
		cursor := " "
		if o.Name == v.selected {
			cursor = ">"
		}
		// At narrow widths keep textual state before the clipped safe identity.
		state, _, _ := v.outputSummary(o)
		lines = append(lines, fmt.Sprintf("%s %s  %s", cursor, safeText(state), safeText(o.Name)))
	}
	if start > 0 || start+v.rows < len(v.visible) {
		lines = append(lines, fmt.Sprintf("Showing %d-%d of %d; PgUp/PgDn", start+1, min(len(v.visible), start+v.rows), len(v.visible)))
	}
	return lines
}

// Reserve the pagination row and only the active search/filter captions.
func (v *View) commandRowCapacity(available int) int {
	captions := 1
	if v.query != "" {
		captions++
	}
	if v.filter != 0 {
		captions++
	}
	return max(1, available-captions)
}

func (v *View) hasActivity() bool {
	return v.live || len(v.snapshot.Activity) > 0 || v.snapshot.Dropped > 0
}

func (v *View) tabCount() int {
	if v.hasActivity() {
		return 3
	}
	return 2
}

func (v *View) availableFilters() []int {
	if !v.commandMode() {
		return []int{0, 1, 2}
	}
	filters := []int{0}
	if slices.ContainsFunc(v.snapshot.Items, func(item progress.Item) bool {
		return item.State == "changed" || item.State == "added" || item.State == "removed"
	}) {
		filters = append(filters, 1)
	}
	if slices.ContainsFunc(v.snapshot.Items, func(item progress.Item) bool { return item.State == "failed" }) {
		filters = append(filters, 2)
	}
	return filters
}

func (v *View) filterName() string {
	if v.commandMode() {
		return []string{"All", "Changed", "Failed"}[v.filter]
	}
	return []string{"All", "Active", "Failed"}[v.filter]
}

func (v *View) detailLines(now time.Time) []string {
	if v.commandMode() {
		return v.commandDetails()
	}
	v.links = nil
	var details []string
	entry := "No selected output"
	if v.operation != "" {
		for _, o := range v.snapshot.Operations {
			if o.ID == v.operation {
				entry = "Operation: " + safeText(o.ID)
				details = append(details, "State: "+safeText(o.State), "Stage: "+safeText(o.Kind), "Updated: "+age(now, o.UpdatedAt))
				if o.Code != "" {
					details = append(details, "Code: "+safeText(o.Code))
				}
				v.links = append(v.links, o.Dependencies...)
				v.links = append(v.links, o.Outputs...)
				break
			}
		}
	} else if o, ok := v.selectedOutput(); ok {
		entry = "Output: " + safeText(o.Name)
		state, stage, _ := v.outputSummary(o)
		outputStage := o.Stage
		if outputStage == "" {
			outputStage = "unavailable"
		}
		if stage == "" {
			stage = "stage unavailable"
		}
		details = append(details, "Platform: "+safeText(o.Platform), "State: "+safeText(o.State), "Stage: "+safeText(outputStage), "Updated: "+age(now, o.UpdatedAt), "Producer: "+safeText(state)+"; "+safeText(stage))
		if v.snapshot.Mode == "recorded" && o.RecordedDigest != "" {
			details = append(details, "Recorded digest: "+safeText(o.RecordedDigest))
		}
		if !slices.ContainsFunc(v.visible, func(row progress.Output) bool { return row.Name == o.Name }) {
			details = append(details, "Outside current filter; selection remains pinned")
		}
		v.links = append(v.links, o.Dependencies...)
		v.links = append(v.links, o.Operations...)
	}
	v.clampLinkIndex()
	lines := []string{entry}
	if len(v.back) > 0 {
		parent := v.back[len(v.back)-1]
		ref := parent.output
		if parent.operation != "" {
			ref = parent.operation
		}
		current := v.selected
		if v.operation != "" {
			current = v.operation
		}
		lines = append(lines, "Path: "+safeText(ref)+" > "+safeText(current), fmt.Sprintf("Drill depth: %d; Esc back", len(v.back)))
	}
	lines = append(lines, "", fmt.Sprintf("Dependencies / operations: %d; Enter follows, Esc back", len(v.links)), strings.Repeat("-", 24))
	for i, ref := range v.links {
		cursor := " "
		if i == v.linkIndex {
			cursor = ">"
		}
		lines = append(lines, cursor+" "+safeText(ref))
	}
	if len(v.links) == 0 {
		lines = append(lines, "No dependencies or operations supplied")
	}
	lines = append(lines, "", "Details", strings.Repeat("-", 24))
	lines = append(lines, details...)
	return append(lines, "", "Command context", strings.Repeat("-", 24), "Plan identity: "+safeText(v.snapshot.PlanDigest))
}

func (v *View) activityLines() []string {
	activity := v.snapshot.Activity
	label := "Following newest"
	if v.commandMode() {
		label = "Result activity (static)"
	}
	if !v.follow {
		label = "Following paused; End resumes"
		if v.commandMode() {
			label = "Result history; End shows latest supplied entry"
		}
	}
	lines := []string{"Activity: " + label}
	if v.commandMode() && len(activity) == 0 {
		return append(lines, "No activity supplied; the operation has already returned.")
	}
	if !v.live && len(activity) == 0 {
		return []string{"Activity", "Activity unavailable; this is a read-only view."}
	}
	if v.snapshot.Dropped > 0 {
		lines = append(lines, fmt.Sprintf("History truncated: %d entries omitted", v.snapshot.Dropped))
	}
	if v.follow {
		v.activityStart = max(0, len(activity)-v.rows)
	} else if len(activity) > 0 {
		index := slices.IndexFunc(activity, func(a progress.Activity) bool { return a.Sequence == v.anchor })
		if index < 0 {
			v.activityStart = 0
			lines = append(lines, "Paused entry evicted; showing oldest retained entry")
		} else {
			v.activityStart = index
		}
	}
	v.activityStart = min(max(0, len(activity)-1), v.activityStart)
	for _, a := range activity[v.activityStart:min(len(activity), v.activityStart+v.rows)] {
		lines = append(lines, safeText(a.Text))
	}
	return lines
}

func (v *View) helpLines() []string {
	quit := "q: confirm closing viewer; Enter / N / Esc stay"
	if v.live {
		quit = "q: confirm canceling Run; Ctrl-C: cancel immediately"
	}
	navigation := "Tab / Shift-Tab or 1/2: Results / Details"
	if !v.commandMode() {
		navigation = "Tab / Shift-Tab or 1/2: Outputs / Details"
	}
	if v.hasActivity() {
		navigation = strings.Replace(navigation, "1/2:", "1/2/3:", 1) + " / Activity; a: Activity"
	}
	filters := v.availableFilters()
	names := make([]string, 0, len(filters))
	for _, filter := range filters {
		label := []string{"All", "Active", "Failed"}[filter]
		if v.commandMode() {
			label = []string{"All", "Changed", "Failed"}[filter]
		}
		names = append(names, label)
	}
	search := "/: name/identity search; printable q is query text"
	if v.commandMode() {
		search = "/: search names, kinds and metadata; printable q is query text"
	}
	lines := []string{"Help; Esc returns", quit, navigation, "Up/Down: select entry, dependency or scroll focused pane", "PgUp/PgDn: page focused pane", "Enter: inspect selected entry or follow dependency", "Esc: Back; in list clear search/filter", search}
	if len(filters) > 1 {
		lines = append(lines, "f: "+strings.Join(names, " / "))
	}
	if v.hasOverview() {
		lines = append(lines, "g: overview / all supplied entries")
	}
	if v.hasActivity() {
		lines = append(lines, "End in Activity: latest supplied entry")
	}
	if v.commandMode() {
		return append(lines, "Fields are projected owning results; no work is rerun")
	}
	return append(lines, "Observations are descriptions, not Release verification")
}

func (v *View) listTitle() string {
	if !v.commandMode() {
		return "Outputs · ↑/↓ select"
	}
	if v.overview() {
		return "Overview · ↑/↓ select"
	}
	if v.hasOverview() {
		return "All entries · ↑/↓ select"
	}
	return "Results · ↑/↓ select"
}

func (v *View) tabs(width int) string {
	labels := []string{"1 Outputs", "2 Details", "3 Activity"}
	if v.commandMode() {
		labels[0] = "1 Results"
		if v.hasOverview() {
			labels[0] = "1 All entries"
			if v.overview() {
				labels[0] = "1 Overview"
			}
		}
	}
	if width < 40 {
		labels = []string{"1 O", "2 D", "3 A"}
		if v.commandMode() {
			labels[0] = "1 R"
		}
	}
	labels = labels[:v.tabCount()]
	labels[v.focus] = "[" + labels[v.focus] + "]"
	return strings.Join(labels, " | ")
}

// Draw uses the existing stack's grapheme/cell clipping. It does not pad or
// truncate Unicode using byte/rune counts or update widgets from producers.
func (v *View) Draw(screen tcell.Screen, snapshot progress.Snapshot, now time.Time, canceling bool) {
	v.update(snapshot)
	if canceling || v.live && (snapshot.Status == "complete" || snapshot.Status == "failed") {
		v.dismissConfirmation()
	}
	width, height := screen.Size()
	v.browseBlocked = width < 40 || height < 12
	if width <= 0 || height <= 0 {
		return
	}
	palette := tuitheme.Colors(v.noColor)
	screen.SetStyle(tcell.StyleDefault.Background(palette.Background).Foreground(palette.Foreground))
	screen.Clear()
	color := palette.Accent
	paint := func(lines []string, x, y, w, h int, scroll int, wrap bool, title string, focused bool) int {
		if w <= 0 || h <= 0 {
			return 0
		}
		view := tview.NewTextView().SetDynamicColors(true).SetTextColor(palette.Foreground).SetWrap(wrap)
		// TextView overrides Box.SetBackgroundColor to reset both the pane
		// fill and its text style; neither should inherit tview's black theme.
		view.SetBackgroundColor(palette.Background)
		if title != "" && w >= 4 && h >= 4 {
			border := palette.Border
			if focused {
				border = palette.Accent
			}
			view.SetBorder(true).SetBorderColor(border).SetTitle(" " + tview.Escape(title) + " ").SetTitleColor(palette.Accent)
		}
		escaped := make([]string, 0, len(lines))
		for _, line := range lines {
			text := tview.Escape(line)
			if !v.noColor {
				var tint tcell.Color
				switch {
				case strings.HasPrefix(line, "NVKR "), strings.HasPrefix(line, "> "):
					tint = palette.Accent
				case strings.HasPrefix(line, "Code: "):
					tint = palette.Failure
				case strings.HasPrefix(line, "Lane: "), strings.HasPrefix(line, "Recorded observations;"), snapshot.Note != "" && line == safeText(snapshot.Note):
					tint = palette.Warning
				}
				if tint != 0 {
					emphasis := ""
					if tint == palette.Accent || tint == palette.Failure {
						emphasis = "b"
					}
					text = fmt.Sprintf("[%s::%s]%s[-::-]", tint.String(), emphasis, text)
				}
			}
			escaped = append(escaped, text)
		}
		text := strings.Join(escaped, "\n")
		view.SetText(text)
		view.SetRect(x, y, w, h)
		_, _, innerWidth, innerHeight := view.GetInnerRect()
		wrapped := tview.WordWrap(text, innerWidth)
		view.ScrollTo(min(max(0, scroll), max(0, len(wrapped)-innerHeight)), 0)
		view.Draw(screen)
		actual, _ := view.GetScrollOffset()
		return actual
	}
	draw := func(lines []string, x, y, w, h int, scroll int, wrap bool) int {
		return paint(lines, x, y, w, h, scroll, wrap, "", false)
	}
	pane := func(lines []string, title string, focused bool, x, y, w, h int, scroll int, wrap bool) int {
		return paint(lines, x, y, w, h, scroll, wrap, title, focused)
	}
	header := v.header(now, canceling)
	if (width < 40 || height < 12) && !v.commandMode() {
		observed := 0
		for _, o := range snapshot.Outputs {
			if o.State == "observed" {
				observed++
			}
		}
		state := v.stateLabel()
		if canceling {
			state = "Canceling"
		}
		membership := fmt.Sprintf("%d/%d observed | %s", observed, len(snapshot.Outputs), state)
		if snapshot.Status == "unavailable" {
			membership = "Membership unknown"
			if v.live && len(snapshot.Outputs) > 0 {
				membership = fmt.Sprintf("Required %d; obs?", len(snapshot.Outputs))
			}
		}
		header = []string{"NVKR " + safeText(v.modeLabel()), membership, fmt.Sprintf("40x12 min; now %dx%d", width, height)}
		if snapshot.FailureCode != "" {
			header = []string{membership}
			header = append(header, tview.WordWrap("Code: "+safeText(snapshot.FailureCode), width)...)
		}
	}
	headerHeight := min(len(header), max(1, height-1))
	if v.commandMode() && (width < 40 || height < 12) {
		header = v.commandTinyHeader(width)
		headerHeight = min(len(header), max(1, height-2))
		for y, line := range header[:headerHeight] {
			tview.Print(screen, tview.Escape(line), 0, y, width, tview.AlignLeft, tcell.ColorDefault)
		}
	} else if v.commandMode() {
		// Measure escaped text with the terminal stack so literal tag-looking
		// fields are never interpreted as invisible markup during wrapping.
		headerHeight = min(len(tview.WordWrap(tview.Escape(strings.Join(header, "\n")), width)), max(1, height-3))
		draw(header, 0, 0, width, headerHeight, 0, true)
	} else {
		for y, line := range header[:min(len(header), headerHeight)] {
			text, tint := tview.Escape(line), palette.Foreground
			switch y {
			case 0:
				tint = palette.Accent
				if !v.noColor {
					text = "[::b]" + text + "[::-]"
				}
			case 2:
				tint = palette.Warning
				if snapshot.FailureCode != "" {
					tint = palette.Failure
				}
			}
			tview.Print(screen, text, 0, y, width, tview.AlignLeft, tint)
		}
	}
	quit := "q confirm close"
	if v.live {
		quit = "q confirm cancel | Ctrl-C cancel"
	}
	footer := "Enter details | / search | " + quit + " | ? help"
	if v.hasOverview() {
		footer = "Enter details | / search | g all entries | " + quit + " | ? help"
		if !v.overview() {
			footer = "Enter details | / search | g overview | " + quit + " | ? help"
		}
	}
	if v.focus == 1 {
		footer = "Arrows select/scroll | Esc back | " + quit + " | ? help"
	}
	if v.focus == 2 {
		footer = "Arrows scroll | End latest | " + quit + " | ? help"
	}
	if width < 80 {
		footer = "Enter details | " + quit + " | ? help"
		if v.hasOverview() {
			target := "all"
			if !v.overview() {
				target = "overview"
			}
			footer = "Enter details | g " + target + " | " + quit + " | ? help"
		}
		if v.focus != 0 {
			footer = "Esc back | " + quit + " | ? help"
		}
	}
	if v.browseBlocked {
		if v.live {
			footer = "q confirm; ^C cancel"
		} else {
			footer = "q confirm close"
		}
	}
	if canceling {
		footer = "Canceling; waiting for owner; no force exit"
	}
	if v.searching && !v.browseBlocked {
		footer = "Search: " + safeText(v.query) + "  Enter apply / Esc dismiss"
	}
	if v.confirmQuit {
		footer = "Close viewer? y / N stay"
		if v.live {
			footer = "Cancel Run? y / N stay"
		}
		if width < 40 {
			footer = "Close? y / N stay"
			if v.live {
				footer = "Cancel? y / N stay"
			}
		}
	}
	tview.Print(screen, tview.Escape(footer), 0, height-1, width, tview.AlignLeft, color)
	contentTop := headerHeight
	if contentTop < height-1 {
		tview.Print(screen, tview.Escape(v.tabs(width)), 0, contentTop, width, tview.AlignLeft, color)
		contentTop++
	}
	hintHeight := 0
	var linkHint []string
	var details []string
	if v.focus == 1 && width >= 40 && height >= 12 && !v.help {
		details = v.detailLines(now)
	}
	if v.focus == 1 && width >= 40 && height >= 12 && !v.confirmQuit && !v.searching && !v.help {
		if len(v.links) > 0 {
			ref := v.links[min(v.linkIndex, len(v.links)-1)]
			linkHint = []string{fmt.Sprintf("Enter follows %d/%d: %s", v.linkIndex+1, len(v.links), safeText(v.linkName(ref)))}
			hintHeight = min(2, len(tview.WordWrap(tview.Escape(linkHint[0]), width)))
		}
	}
	available := max(0, height-contentTop-1-hintHeight)
	innerAvailable := available
	if available >= 4 {
		innerAvailable -= 2 // Frame rows are not selectable or scrollable content.
	}
	v.rows = max(1, available-4)
	confirmation := func() {
		if !v.confirmQuit || available <= 0 {
			return
		}
		title := "Close this read-only viewer?"
		explanation := "The record and its owner remain unchanged."
		if v.live {
			title = "Cancel the current Run?"
			explanation = "Request cancellation and wait for the owner to stop."
		}
		choices := "y confirm | N/Enter/Esc Stay (default)"
		lines := []string{strings.Repeat("-", width), title, explanation, choices}
		if available >= 6 && width >= 40 {
			lines = lines[1:]
			h := len(tview.WordWrap(tview.Escape(strings.Join(lines, "\n")), width-2)) + 2
			if h > available {
				// Keep all consent choices visible before optional explanation.
				lines = []string{title, choices}
				h = len(tview.WordWrap(tview.Escape(strings.Join(lines, "\n")), width-2)) + 2
			}
			pane(lines, "Confirmation", true, 0, contentTop+available-h, width, h, 0, true)
			return
		}
		if available < 4 {
			title = "Close viewer?"
			if v.live {
				title = "Cancel Run?"
			}
			lines = []string{title}
		}
		h := min(available, len(tview.WordWrap(strings.Join(lines, "\n"), width)))
		draw(lines, 0, contentTop+available-h, width, h, 0, true)
	}
	if width < 40 || height < 12 {
		draw([]string{fmt.Sprintf("40x12 min; now %dx%d; work unchanged", width, height)}, 0, contentTop, width, available, 0, true)
		confirmation()
		return
	}
	if v.help {
		v.rows = max(1, innerAvailable)
		v.helpOffset = pane(v.helpLines(), "Help · Esc returns", true, 0, contentTop, width, available, v.helpOffset, true)
		confirmation()
		return
	}
	if width >= 120 && height >= 30 && v.focus == 1 {
		left := (width - 1) / 3
		right := left + 1
		v.rows = v.commandRowCapacity(innerAvailable)
		if !v.commandMode() {
			v.rows = max(1, innerAvailable-3)
		}
		pane(v.outputLines(left-2), v.listTitle(), false, 0, contentTop, left, available, 0, false)
		v.detailOffset = pane(details, "Details · Enter follows, Esc back", true, right, contentTop, width-right, available, v.detailOffset, true)
	} else {
		v.rows = v.commandRowCapacity(innerAvailable)
		if !v.commandMode() {
			v.rows = max(1, innerAvailable-3)
		}
		switch v.focus {
		case 0:
			pane(v.outputLines(width-2), v.listTitle(), true, 0, contentTop, width, available, 0, false)
		case 1:
			v.detailOffset = pane(details, "Details · Enter follows, Esc back", true, 0, contentTop, width, available, v.detailOffset, true)
		case 2:
			pane(v.activityLines(), "Activity · public descriptions", true, 0, contentTop, width, available, 0, true)
		}
	}
	// The selected dependency is pinned above the controls, so scrolling a
	// long detail page cannot conceal the target Enter will follow.
	if hintHeight > 0 {
		draw(linkHint, 0, height-1-hintHeight, width, hintHeight, 0, true)
	}

	confirmation()
}
