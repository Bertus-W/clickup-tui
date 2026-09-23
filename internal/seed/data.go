// Package seed holds the demo project: Scope/Severity fields and a set of realistic tasks.
package seed

type Choice struct{ Name, Color string }

// Fields are the dropdown custom fields of the demo list, with their colours.
var Fields = []struct {
	Name    string
	Options []Choice
}{
	{"Scope", []Choice{{"Frontend", "#3397dd"}, {"Backend", "#9b59b6"}, {"Infra", "#ff7800"}, {"Design", "#ff4081"}, {"Docs", "#2ecd6f"}}},
	{"Severity", []Choice{{"Critical", "#e50000"}, {"Major", "#ff7800"}, {"Minor", "#f9d900"}, {"Trivial", "#667684"}}},
}

type Task struct {
	Name, Scope, Severity string
	Priority              int  // 1 urgent … 4 low, 0 none
	DueInDays             *int // nil: no due date
	Status                string
	Mine                  bool
	Description           string
	Subtasks              []string
}

func days(n int) *int { return new(n) }

var Tasks = []Task{
	{"Login redirect loop on Safari 18", "Frontend", "Critical", 1, days(1), "in progress", true,
		"Users on Safari 18 get bounced between `/login` and `/auth/callback` after SSO.\n\n" +
			"## Repro\n1. Log in with Google\n2. Watch the redirect loop\n\n" +
			"Likely cause: `SameSite=None` cookie without `Secure` on the staging domain.",
		[]string{"Reproduce on BrowserStack", "Set Secure flag on session cookie"}},
	{"Billing webhook retries double-charge customers", "Backend", "Critical", 1, days(0), "to do", true,
		"Stripe retries `invoice.paid` when we answer after 10s; our handler is not idempotent.", nil},
	{"p95 task search latency > 4s", "Backend", "Major", 2, days(5), "in progress", false,
		"Search does a sequential scan on `tasks.name`. Add a trigram index and measure again.", nil},
	{"Dark mode contrast on settings page", "Frontend", "Minor", 3, days(10), "to do", false,
		"Secondary text fails WCAG AA in dark mode (contrast 3.1:1).", nil},
	{"Upgrade Postgres 15 → 17", "Infra", "Major", 2, days(14), "to do", true,
		"Plan: logical replication to a new cluster, then switch over during the maintenance window.",
		[]string{"Dry run on staging", "Schedule maintenance window", "Update runbook"}},
	{"Flaky e2e: checkout flow", "Frontend", "Minor", 3, nil, "to do", false,
		"Fails ~1 in 20 runs waiting for the payment iframe.", nil},
	{"Rate-limit the public API", "Backend", "Major", 2, days(21), "to do", false,
		"Token bucket per API key: 100 req/min, burst 20. Return `429` with `Retry-After`.", nil},
	{"New onboarding checklist visuals", "Design", "Trivial", 4, days(30), "to do", false,
		"Illustrations for the 5 onboarding steps. Keep them under 20 KB each.", nil},
	{"Document the webhook signature scheme", "Docs", "Minor", 3, days(7), "to do", true,
		"Explain HMAC-SHA256 signing, the timestamp header and replay protection.", nil},
	{"Terraform state drift in eu-west", "Infra", "Minor", 3, nil, "in progress", false,
		"`terraform plan` shows drift on two security groups edited by hand.", nil},
	{"Mobile nav design review", "Design", "Trivial", 0, days(3), "complete", false,
		"Review done; feedback captured in the design file.", nil},
	{"CSV export for reports", "Backend", "Minor", 3, days(12), "to do", false,
		"Stream the export instead of building it in memory; large accounts have 200k rows.", nil},
}
