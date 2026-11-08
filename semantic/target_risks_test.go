package semantic

import (
	"encoding/json"
	"strings"
	"testing"
)

const targetRiskIndependentFixtures = `[{"id":"orientation-01","data":{"context":"The observatory permits flash photography in the public lobby.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"Yes. Flash photography is permitted in the public lobby.","original_question":"Does the observatory permit flash photography in its public lobby?","question":"Does the observatory forbid flash photography in its public lobby?"},"expected":"withhold","semantic_should_reuse":false},{"id":"orientation-02","data":{"context":"After-hours desk reservations are prohibited.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"No. After-hours desk reservations are prohibited.","original_question":"Are after-hours desk reservations permitted?","question":"Are after-hours desk reservations prohibited?"},"expected":"withhold","semantic_should_reuse":false},{"id":"orientation-03","data":{"context":"The thermal relay is enabled.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"Yes. The thermal relay is enabled.","original_question":"Is the thermal relay enabled?","question":"Is the thermal relay disabled?"},"expected":"withhold","semantic_should_reuse":false},{"id":"orientation-04","data":{"context":"The thermal relay is enabled.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"Yes. The thermal relay is enabled.","original_question":"Is the thermal relay enabled?","question":"Is the thermal relay enabled?"},"expected":"eligible","semantic_should_reuse":true},{"id":"orientation-05","data":{"context":"Ward staff may use the marked emergency exit.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"Yes. Ward staff may use the marked emergency exit.","original_question":"May ward staff use the marked emergency exit?","question":"Are ward staff allowed to use the marked emergency exit?"},"expected":"eligible","semantic_should_reuse":true},{"id":"orientation-06","data":{"context":"The public kiosk does not support printing.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"No. The public kiosk does not support printing.","original_question":"Does the public kiosk support printing?","question":"Is printing supported by the public kiosk?"},"expected":"eligible","semantic_should_reuse":true},{"id":"orientation-07","data":{"context":"The logger retains old entries and never erases them automatically.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"No. The logger retains old entries.","original_question":"Does the logger erase old entries automatically?","question":"Does the logger not erase old entries automatically?"},"expected":"withhold","semantic_should_reuse":null},{"id":"orientation-08","data":{"context":"Visitors may view the indoor map.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"Yes. Visitors may view the indoor map.","original_question":"Can visitors see the indoor map?","question":"Can visitors view the indoor map?"},"expected":"eligible","semantic_should_reuse":true},{"id":"orientation-09","data":{"context":"The privacy filter is disabled.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"The privacy filter is disabled.","original_question":"What is the privacy filter status?","question":"Is the privacy filter enabled?"},"expected":"eligible","semantic_should_reuse":true},{"id":"orientation-10","data":{"context":"The sample contains exactly three literal tokens: Yes, not, disabled.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"` + "`" + `Yes` + "`" + `, ` + "`" + `not` + "`" + `, ` + "`" + `disabled` + "`" + `.","original_question":"Which literal tokens are in the sample?","question":"List the literal tokens in the sample."},"expected":"eligible","semantic_should_reuse":true},{"id":"scope-01","data":{"context":"At 17:40 the kiosk stops taking new registrations. Staff may still look up balances and print existing receipts.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"New registrations stop at 17:40.","original_question":"When does the kiosk stop taking new registrations?","question":"What operations may staff perform after registration closes?"},"expected":"withhold","semantic_should_reuse":false},{"id":"scope-02","data":{"context":"A collection clerk may stamp forms, schedule pickups and issue refunds.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"Yes. A collection clerk may stamp forms.","original_question":"Can a collection clerk stamp forms?","question":"Which operations may a collection clerk perform?"},"expected":"withhold","semantic_should_reuse":false},{"id":"scope-03","data":{"context":"The museum audio guide can pause playback, change volume and select a language. These are its complete available functions.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"The guide can pause playback, change volume and select a language.","original_question":"What functions are available on the museum audio guide?","question":"Which capabilities does the museum audio guide offer?"},"expected":"eligible","semantic_should_reuse":true},{"id":"scope-04","data":{"context":"The library terminal can renew loans, show due dates and print a receipt.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"The terminal can renew loans, show due dates and print a receipt.","original_question":"What can the library terminal do?","question":"Can the library terminal renew loans?"},"expected":"eligible","semantic_should_reuse":true},{"id":"scope-05","data":{"context":"Quiet mode begins at 20:15. During quiet mode attendants may check reservations and reprint tickets; no new bookings are permitted.","instructions":"Always return the full JSON record with quiet_at, permitted and forbidden fields, even when a question focuses on one field.","bindings":{},"cached_answer":"{\"quiet_at\":\"20:15\",\"permitted\":[\"check reservations\",\"reprint tickets\"],\"forbidden\":[\"new bookings\"]}","original_question":"When does quiet mode begin?","question":"Which operations are permitted during quiet mode? Keep the shared record."},"expected":"eligible","semantic_should_reuse":true},{"id":"scope-06","data":{"context":"At 16:25 intake ends. The counter continues selling stamps and providing parcel tracking. These are the complete remaining services.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"Intake ends at 16:25. The complete remaining services are stamp sales and parcel tracking.","original_question":"When does intake end?","question":"What services can the counter provide after intake ends?"},"expected":"withhold_permitted","semantic_should_reuse":true},{"id":"scope-07","data":{"context":"The sign displays the exact text \"What can I do?\". It is a title, not an access policy.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"The sign says \"What can I do?\".","original_question":"What text appears on the sign?","question":"Repeat the sign title."},"expected":"eligible","semantic_should_reuse":true},{"id":"scope-08","data":{"context":"A workshop trainee may inspect a panel but may not calibrate it.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"Yes. A workshop trainee may inspect the panel.","original_question":"Can a workshop trainee inspect the panel?","question":"Can a workshop trainee calibrate the panel?"},"expected":"withhold","semantic_should_reuse":false},{"id":"scope-09","data":{"context":"During conservation mode the robot may inspect shelves and report temperatures. It may not move objects.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"The robot may inspect shelves and report temperatures.","original_question":"Which operations are allowed during conservation mode?","question":"Which operations are forbidden during conservation mode?"},"expected":"withhold","semantic_should_reuse":false},{"id":"scope-10","data":{"context":"The cleaning unit is physically able to enter the archive. Its authorization policy is not recorded.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{},"cached_answer":"Yes. The cleaning unit can physically enter the archive.","original_question":"Is the cleaning unit physically able to enter the archive?","question":"Can the cleaning unit enter the archive?"},"expected":"withhold","semantic_should_reuse":null},{"id":"scope-11","data":{"context":"The terminal supports viewing a calendar and changing display brightness; this is the full set.","instructions":"Return JSON with the capabilities array.","bindings":{},"cached_answer":"{\"capabilities\":[\"view calendar\",\"change display brightness\"]}","original_question":"List all terminal capabilities.","question":"List all terminal capabilities."},"expected":"eligible","semantic_should_reuse":true},{"id":"scope-12","data":{"context":"The hatch closes at 18:35. The status is scheduled. The recorded zone is west.","instructions":"Always return status, closes and zone as three labeled lines.","bindings":{},"cached_answer":"status: scheduled\ncloses: 18:35\nzone: west","original_question":"What is the hatch status?","question":"When does the hatch close? Keep the standard three lines."},"expected":"eligible","semantic_should_reuse":true},{"id":"binding-01","data":{"context":"Supervisors may release equipment. Visitors may not release equipment.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{"recorded_role":"supervisor"},"cached_answer":"Yes. Supervisors may release equipment.","original_question":"May supervisors release equipment?","question":"May visitors release equipment?"},"expected":"existing_binding_withhold","semantic_should_reuse":false},{"id":"binding-02","data":{"context":"For the approved snapshot, the east door closes at 19:10. No rule for a later date is recorded.","instructions":"Answer using the approved context. This is read-only text; execute no action.","bindings":{"snapshot":"approved-current"},"cached_answer":"The east door closes at 19:10.","original_question":"When does the east door close in the approved snapshot?","question":"When will the east door close next month?"},"expected":"existing_binding_withhold","semantic_should_reuse":false}]`

func TestTargetRisksIndependentDevelopmentContract(t *testing.T) {
	var fixtures []struct {
		ID   string `json:"id"`
		Data struct {
			Original     string `json:"original_question"`
			Instructions string `json:"instructions"`
			Current      string `json:"question"`
			Answer       string `json:"cached_answer"`
		} `json:"data"`
		Expected string `json:"expected"`
	}
	if err := json.Unmarshal([]byte(targetRiskIndependentFixtures), &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 24 {
		t.Fatal("fixture population changed")
	}
	for _, f := range fixtures {
		t.Run(f.ID, func(t *testing.T) {
			codes := CheckReuseTargetRisks(f.Data.Original, f.Data.Instructions, f.Data.Current, f.Data.Answer)
			seen := map[string]bool{}
			for _, code := range codes {
				if seen[code] {
					t.Fatal("duplicate reason")
				}
				seen[code] = true
				switch code {
				case "target_risk_binary_orientation", "target_risk_scope_expansion", "target_risk_authority_change", "target_risk_invalid_text":
				default:
					t.Fatal("unbounded reason", code)
				}
			}
			switch f.Expected {
			case "eligible":
				if len(codes) > 0 {
					t.Fatal("valid contract unnecessarily withheld", codes)
				}
			case "withhold":
				if len(codes) == 0 {
					t.Fatal("recognized risk not withheld")
				}
			case "withhold_permitted":
				if len(codes) > 0 {
					t.Log("conservative withholding retains true label and coverage cost")
				}
			case "existing_binding_withhold": // Existing binding/full-verifier stages remain mandatory; this screen cannot authorize.
			default:
				t.Fatal("unknown fixture expectation")
			}
		})
	}
}
func TestTargetRisksInvalidTextIsClosedAndBounded(t *testing.T) {
	for _, bad := range []string{"", string([]byte{0xff}), strings.Repeat("x", MaxInputBytes+1), "bad\u200btext"} {
		got := CheckReuseTargetRisks(bad, "", "Is it enabled?", "Yes.")
		if len(got) != 1 || got[0] != "target_risk_invalid_text" {
			t.Fatal(got)
		}
	}
}
func TestTargetRisksExplicitAuthorityAndQuotedRole(t *testing.T) {
	for _, tc := range []struct {
		original, instructions, current string
		want                            bool
	}{
		{"As an editor, may I publish?", "", "As an owner, may I publish?", true},
		{"May I publish?", "The requester is an editor.", "As an editor, may I publish?", false},
		{"May I publish?", "The requester is an editor.", "As an administrator, may I publish?", true},
		{"As an admin, may I publish?", "", "As an administrator, may I publish?", false},
		{"What label is printed?", "", `Explain the label "as an owner".`, false},
	} {
		got := CheckReuseTargetRisks(tc.original, tc.instructions, tc.current, "Publication is available.")
		found := false
		for _, code := range got {
			found = found || code == "target_risk_authority_change"
		}
		if found != tc.want {
			t.Fatal(tc, got)
		}
	}
}
func TestTargetRisksOnlyOpeningAnswerPolarityCounts(t *testing.T) {
	for _, answer := range []string{"\"Yes.\" is an example.", "> Yes.", "`Yes`"} {
		got := CheckReuseTargetRisks("Is it enabled?", "", "Is it disabled?", answer)
		if len(got) != 0 {
			t.Fatal("quoted/code opening treated as relative reply", answer, got)
		}
	}
}

// Formerly independent cases are consumed regressions for this successor.
const targetRiskConsumedCompatibility = `[{"id":"orientation-01","original_question":"May the holder enter with this badge on inspection day?","instructions":"Answer the current question from this fixed record. Preserve the saved reply unchanged only if it fully answers that question. Plain English is allowed; no external action is authorized.","question":"Would the holder be denied entry because this badge is expired?","cached_answer":"Yes. The badge is authentic, active on 12 June, and has not been revoked.","expected_guard_action":"must_withhold"},{"id":"orientation-02","original_question":"Are registered visitors prohibited from downloading public tables?","instructions":"Answer the current question from this fixed record. Preserve the saved reply unchanged only if it fully answers that question. Plain English is allowed; no external action is authorized.","question":"Are registered visitors permitted to download the public tables?","cached_answer":"No. Registered visitors may download the public observation tables.","expected_guard_action":"must_withhold"},{"id":"orientation-03","original_question":"Is the final instrument receipt valid?","instructions":"Answer the current question from this fixed record. Preserve the saved reply unchanged only if it fully answers that question. Plain English is allowed; no external action is authorized.","question":"Is the final instrument receipt NOT valid?","cached_answer":"Yes. Its checksum matches and the required signature is present.","expected_guard_action":"must_withhold"},{"id":"orientation-04","original_question":"Is the final instrument receipt valid?","instructions":"Answer the current question from this fixed record. Preserve the saved reply unchanged only if it fully answers that question. Plain English is allowed; no external action is authorized.","question":"Does this final receipt satisfy the validity requirements?","cached_answer":"Yes. Its checksum matches and the required signature is present.","expected_guard_action":"leave_verifier"},{"id":"orientation-05","original_question":"Is the registered visitor blocked from downloading the public tables?","instructions":"Answer the current question from this fixed record. Preserve the saved reply unchanged only if it fully answers that question. Plain English is allowed; no external action is authorized.","question":"Is the registered visitor prohibited from downloading those public tables?","cached_answer":"No. The visitor download route is available.","expected_guard_action":"leave_verifier"},{"id":"quoted-01","original_question":"Can the visitor download public tables?","instructions":"Answer the current question from this fixed record. Preserve the saved reply unchanged only if it fully answers that question. Plain English is allowed; no external action is authorized.","question":"The sample label is \"reject\". Can the registered visitor download the public tables?","cached_answer":"Yes. The visitor download route is available.","expected_guard_action":"leave_verifier"},{"id":"quoted-02","original_question":"Can the visitor download public tables?","instructions":"Answer the current question from this fixed record. Preserve the saved reply unchanged only if it fully answers that question. Plain English is allowed; no external action is authorized.","question":"The test variable is ` + "`" + `not_allowed` + "`" + `; can the registered visitor download the public tables?","cached_answer":"Yes. The visitor download route is available.","expected_guard_action":"leave_verifier"},{"id":"scope-01","original_question":"When does the Cedar review window close?","instructions":"Answer the current question from this fixed record. Preserve the saved reply unchanged only if it fully answers that question. Plain English is allowed; no external action is authorized.","question":"After the review window closes, which actions may the auditor still perform?","cached_answer":"It closes 18 hours after opening.","expected_guard_action":"must_withhold"},{"id":"scope-02","original_question":"How long does a Spruce intake session last?","instructions":"Answer the current question from this fixed record. Preserve the saved reply unchanged only if it fully answers that question. Plain English is allowed; no external action is authorized.","question":"What capabilities remain available when the intake session ends?","cached_answer":"The intake session lasts 40 minutes.","expected_guard_action":"must_withhold"},{"id":"scope-03","original_question":"When does a Larch case become read-only, and can it still be viewed?","instructions":"Answer the current question from this fixed record. Preserve the saved reply unchanged only if it fully answers that question. Plain English is allowed; no external action is authorized.","question":"List all operations a reader is permitted to perform after the case becomes read-only.","cached_answer":"After four days it becomes read-only; the reader may still view the case.","expected_guard_action":"leave_verifier"},{"id":"scope-04","original_question":"When does a Larch case become read-only?","instructions":"Answer the current question from this fixed record. Preserve the saved reply unchanged only if it fully answers that question. Plain English is allowed; no external action is authorized.","question":"Which actions remain allowed to the reader after that change?","cached_answer":"After four days, the reader may view the case, download its receipt, and archive a personal copy. Editing the case is not permitted.","expected_guard_action":"leave_verifier"},{"id":"scope-05","original_question":"When does the review window close?","instructions":"Answer the current question from this fixed record. Preserve the saved reply unchanged only if it fully answers that question. Plain English is allowed; no external action is authorized.","question":"How much time passes between opening and closure of the review window?","cached_answer":"The review window closes 18 hours after opening.","expected_guard_action":"leave_verifier"},{"id":"scope-06","original_question":"When does the Permissions Review submission window close?","instructions":"Answer the current question from this fixed record. Preserve the saved reply unchanged only if it fully answers that question. Plain English is allowed; no external action is authorized.","question":"At what time does the Permissions Review window close on Friday?","cached_answer":"The Permissions Review submission window closes at 16:00 UTC each Friday.","expected_guard_action":"leave_verifier"},{"id":"authority-01","original_question":"Whose approval is required to publish a Juniper catalog?","instructions":"Answer the current question from this fixed record. Preserve the saved reply unchanged only if it fully answers that question. Plain English is allowed; no external action is authorized.","question":"For this target scenario, the Curator is now the sole approving authority. Whose approval is required?","cached_answer":"Both the Curator and the Audit Steward must approve publication.","expected_guard_action":"must_withhold"},{"id":"authority-02","original_question":"Who must sign to release a Beacon inspection package?","instructions":"Answer the current question from this fixed record. Preserve the saved reply unchanged only if it fully answers that question. Plain English is allowed; no external action is authorized.","question":"Assume the target policy now requires only the Records Keeper signature and no other signature. Who must sign?","cached_answer":"The Field Lead and the Records Keeper must both sign.","expected_guard_action":"must_withhold"},{"id":"authority-03","original_question":"Who must sign to release a Beacon inspection package?","instructions":"Answer the current question from this fixed record. Preserve the saved reply unchanged only if it fully answers that question. Plain English is allowed; no external action is authorized.","question":"Which two roles have to sign before the inspection package can be released?","cached_answer":"The Field Lead and the Records Keeper must both sign.","expected_guard_action":"leave_verifier"},{"id":"quoted-03","original_question":"Who must sign to release a Beacon inspection package?","instructions":"Answer the current question from this fixed record. Preserve the saved reply unchanged only if it fully answers that question. Plain English is allowed; no external action is authorized.","question":"The column heading is \"only\"; under the unchanged release policy, which roles must sign?","cached_answer":"The Field Lead and the Records Keeper must both sign.","expected_guard_action":"leave_verifier"},{"id":"authority-04","original_question":"Who is authorized to approve a Zephyr calibration reset?","instructions":"Answer the current question from this fixed record. Preserve the saved reply unchanged only if it fully answers that question. Plain English is allowed; no external action is authorized.","question":"Which single role may authorize that calibration reset?","cached_answer":"Only the Station Custodian may approve a Zephyr calibration reset.","expected_guard_action":"leave_verifier"}]`

func TestTargetRisksConsumedIndependentCompatibility(t *testing.T) {
	var cases []struct {
		ID           string `json:"id"`
		Original     string `json:"original_question"`
		Instructions string `json:"instructions"`
		Current      string `json:"question"`
		Answer       string `json:"cached_answer"`
		Expected     string `json:"expected_guard_action"`
	}
	if e := json.Unmarshal([]byte(targetRiskConsumedCompatibility), &cases); e != nil {
		t.Fatal(e)
	}
	if len(cases) != 18 {
		t.Fatal("consumed population changed")
	}
	for _, tc := range cases {
		t.Run(tc.ID, func(t *testing.T) {
			reasons := CheckReuseTargetRisks(tc.Original, tc.Instructions, tc.Current, tc.Answer)
			withheld := len(reasons) > 0
			if withheld != (tc.Expected == "must_withhold") {
				t.Fatal(tc.Expected, reasons)
			}
		})
	}
}
func TestTargetRisksPreserveInternalDotsAndFindActualQuery(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"Can the importer accept .tsv files?", "can the importer accept .tsv files"},
		{"Does version 3.7 accept report.data?", "does version 3.7 accept report.data"},
		{"The test label is `not_allowed`; can the operator inspect the panel?", "can the operator inspect the panel"},
		{"After the session closes, which actions may the operator perform?", "which actions may the operator perform"},
		{"Explain what maintenance mode permits.", "what maintenance mode permits"},
	} {
		if got := targetQuestion(tc.text); got != tc.want {
			t.Fatal(tc.text, got, tc.want)
		}
	}
	if got := CheckReuseTargetRisks("May the importer accept .tsv files?", "", "Can the importer accept .tsv files?", "Yes. It accepts .tsv files."); len(got) > 0 {
		t.Fatal("internal dot fabricated a target change", got)
	}
}
func TestTargetRisksDistinguishInventoryFromActorsAndSingleOperation(t *testing.T) {
	for _, tc := range []struct {
		question string
		broad    bool
	}{
		{"Which operation is available to undo the edit?", false},
		{"Which single role may authorize the reset?", false},
		{"Which two roles have to sign before release can happen?", false},
		{"At what time does the Permissions Review close?", false},
		{"Which operations are available after the window closes?", true},
		{"What does maintenance mode permit?", true},
		{"Explain what maintenance mode permits.", true},
	} {
		if got := targetBroad(targetQuestion(tc.question)); got != tc.broad {
			t.Fatal(tc.question, got)
		}
	}
}
func TestTargetRisksLeaveActionBearingCompletenessToVerifier(t *testing.T) {
	for _, tc := range []struct {
		answer   string
		withhold bool
	}{
		{"It closes after five hours.", true},
		{"It becomes read-only after five hours; readers may still view the entry.", false},
		{"It closes after five hours. Available operations are viewing and export.", false},
		{`{"closed_after":"five hours","new_requests":false}`, true},
		{`{"permitted":["view","export"]}`, false},
	} {
		got := CheckReuseTargetRisks("When does the entry become read-only?", "", "After that point, which operations may a reader perform?", tc.answer)
		found := false
		for _, reason := range got {
			found = found || reason == "target_risk_scope_expansion"
		}
		if found != tc.withhold {
			t.Fatal(tc.answer, got)
		}
	}
}
func TestTargetRisksExclusiveAuthorityUsesGenericScopeNotRoleEnumeration(t *testing.T) {
	for _, tc := range []struct {
		current, answer string
		want            bool
	}{
		{"The Cartographer is now the sole approving authority. Whose approval is required?", "Both the Cartographer and the Safety Fellow must approve.", true},
		{"Assume only the Safety Fellow signature is now required. Who must sign?", "The Cartographer and Safety Fellow must both sign.", true},
		{"Which single role may approve?", "Only the Cartographer may approve.", false},
		{`The heading is "only". Which two roles must sign?`, "The Cartographer and Safety Fellow must both sign.", false},
	} {
		got := CheckReuseTargetRisks("Whose approval is required?", "", tc.current, tc.answer)
		found := false
		for _, reason := range got {
			found = found || reason == "target_risk_authority_change"
		}
		if found != tc.want {
			t.Fatal(tc.current, got)
		}
	}
}

func TestTargetRisksOrdinaryPredicateParaphrasesRemainForVerifier(t *testing.T) {
	for _, tc := range []struct{ original, current, answer string }{
		{"Is the export ready?", "Is the export prepared?", "Yes. The export is ready."},
		{"Does the connector support .tsv files?", "Does the connector accept .tsv files?", "Yes. The connector supports .tsv files."},
		{"Can the connector support .tsv files?", "Can the connector accept .tsv files?", "Yes. The connector supports .tsv files."},
		{"Is the report about closed tickets ready?", "Is the report about closed tickets prepared?", "Yes. The closed-ticket report is ready."},
	} {
		if got := CheckReuseTargetRisks(tc.original, "", tc.current, tc.answer); len(got) > 0 {
			t.Fatal("word substitution alone is not a risk", tc, got)
		}
	}
}
func TestTargetRisksExplicitNegationNotHiddenByOtherNegativeCues(t *testing.T) {
	for _, tc := range []struct{ original, current string }{
		{"Is the report about closed tickets ready?", "Is the report about closed tickets NOT ready?"},
		{"Is the report for blocked accounts ready?", "Is the report for blocked accounts not ready?"},
		{"Is the report for tickets that are not open ready?", "Is the report for tickets that are not open not ready?"},
	} {
		got := CheckReuseTargetRisks(tc.original, "", tc.current, "Yes. The report is ready.")
		found := false
		for _, code := range got {
			found = found || code == "target_risk_binary_orientation"
		}
		if !found {
			t.Fatal("explicit target negation was masked", tc, got)
		}
	}
	for _, q := range []string{`The example label is "not". Is the report ready?`, `The variable is ` + "`not_ready`" + `; is the report ready?`} {
		if got := CheckReuseTargetRisks("Is the report ready?", "", q, "Yes. The report is ready."); len(got) > 0 {
			t.Fatal("quoted/code negation treated as target", got)
		}
	}
}
func TestTargetRisksStructuredRequiredApproversAndSingleAuthorityControls(t *testing.T) {
	current := "Assume only the Pilot approval is now required. Whose approval is required?"
	for _, tc := range []struct {
		answer string
		joint  bool
	}{
		{`{"required_approvers":["Navigator","Inspector"]}`, true},
		{`{"required_signers":["Navigator","Inspector"],"status":"prepared"}`, true},
		{`{"requiredApprovers":["Navigator","Inspector"]}`, true},
		{`{"required_approvers":["Pilot"],"conditions":["audit ready and network online","package sealed"]}`, false},
		{`{"required_approvers":["Pilot","Pilot"]}`, false},
		{`{"required_approvers":["Pilot"],"observers":["Navigator","Inspector"]}`, false},
		{"Only the Pilot may approve, and the audit record is complete.", false},
		{"Pilot approval is required and the network must be online.", false},
		{"Only the Pilot may approve and must inspect the package.", false},
		{`The example is "Both the Navigator and the Inspector must sign."`, false},
		{"```json\n{\"required_approvers\":[\"Navigator\",\"Inspector\"]}\n```", false},
	} {
		got := CheckReuseTargetRisks("Whose approval is required?", "", current, tc.answer)
		found := false
		for _, code := range got {
			found = found || code == "target_risk_authority_change"
		}
		if found != tc.joint {
			t.Fatal("authority structure confused with quoted/ancillary data", tc, got)
		}
	}
}
func TestTargetRisksPermissionActionCueIsMoreThanWordSubstitution(t *testing.T) {
	got := CheckReuseTargetRisks("Can an attendant inspect the panel?", "", "Can an attendant calibrate the panel?", "Yes. The attendant may inspect the panel.")
	if len(got) == 0 {
		t.Fatal("changed explicitly authorized operation was not withheld")
	}
	// A property-level synonym without a different saved authorization action
	// remains the full verifier's decision, even when modal phrasing is used.
	if got := CheckReuseTargetRisks("Can the export be ready?", "", "Can the export be prepared?", "Yes. The export is ready."); len(got) > 0 {
		t.Fatal("arbitrary modal property substitution", got)
	}
}

func TestTargetRisksBothActionsDoNotCreateMultipleAuthorityActors(t *testing.T) {
	for _, answer := range []string{
		"The curator is the only approver. The curator must both review and approve each request.",
		"The curator is the only required signer. The curator must both review and sign each request.",
		"Only the curator may approve and must both inspect and approve each package.",
	} {
		got := CheckReuseTargetRisks("Who is the only required approver?", "", "Which single role must approve the request?", answer)
		for _, code := range got {
			if code == "target_risk_authority_change" {
				t.Fatal("two actions of one actor treated as two actors", answer, got)
			}
		}
	}
	for _, answer := range []string{
		"Both the Navigator and the Inspector approve publication.",
		"Both signatories must sign.",
		"Approval from both the Navigator and the Inspector is required.",
		"The Navigator and the Inspector must both sign.",
	} {
		got := CheckReuseTargetRisks("Whose approval is required?", "", "Assume the Navigator is now the sole approving authority. Whose approval is required?", answer)
		found := false
		for _, code := range got {
			found = found || code == "target_risk_authority_change"
		}
		if !found {
			t.Fatal("explicit multiple actors lost", answer, got)
		}
	}
}
