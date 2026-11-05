package architecture

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/TrianaLab/aval/internal/policy"
	"github.com/TrianaLab/aval/pkg/evidence"
)

const (
	actionsIssuer = "https://token.actions.githubusercontent.com"
	// ownWorkflow prefixes every identity the main policy may trust.
	ownWorkflow = `https://github\.com/TrianaLab/aval/\.github/workflows/`
)

func loadPolicy(t *testing.T, name string) *policy.Policy {
	t.Helper()
	p, err := policy.Load(filepath.Join(root, ".github", "aval", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// mainPolicyProblems: main and releases trust only Aval's own workflows,
// keyless, at runner level, and require runner level (#28).
func mainPolicyProblems(p *policy.Policy) []string {
	var problems []string
	if p.MinTrustLevel != evidence.TrustRunner {
		problems = append(problems, "minTrustLevel must be runner")
	}
	for _, id := range p.Identities {
		if id.PublicKey != "" || id.Issuer != actionsIssuer || !strings.HasPrefix(id.SubjectRegexp, ownWorkflow) || id.TrustLevel != evidence.TrustRunner {
			problems = append(problems, "untrusted identity "+id.Issuer+" "+id.SubjectRegexp)
		}
	}
	return problems
}

// prPolicyProblems: a PR may also accept developer signatures, but grants
// more than developer level only to identities the main policy trusts.
func prPolicyProblems(pr, main *policy.Policy) []string {
	var problems []string
	for _, id := range pr.Identities {
		if id.TrustLevel > evidence.TrustDeveloper && !slices.Contains(main.Identities, id) {
			problems = append(problems, "identity above developer level not in the main policy: "+id.Issuer+" "+id.SubjectRegexp)
		}
	}
	return problems
}

func TestDogfoodPolicies(t *testing.T) {
	main, pr := loadPolicy(t, "main.yaml"), loadPolicy(t, "pr.yaml")
	for _, p := range append(mainPolicyProblems(main), prPolicyProblems(pr, main)...) {
		t.Error(p)
	}
}

func TestDogfoodPolicyChecksCatchLooseTrust(t *testing.T) {
	ci := evidence.Identity{Issuer: actionsIssuer, SubjectRegexp: ownWorkflow + `ci\.yaml@refs/heads/main`, TrustLevel: evidence.TrustRunner}
	main := &policy.Policy{MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{ci}}
	if p := mainPolicyProblems(main); len(p) != 0 {
		t.Fatalf("valid main policy reported %v", p)
	}
	dev := evidence.Identity{Issuer: "https://github.com/login/oauth", SubjectRegexp: `a@example\.com`, TrustLevel: evidence.TrustDeveloper}
	bad := map[string]*policy.Policy{
		"developer level":     {MinTrustLevel: evidence.TrustDeveloper, Identities: []evidence.Identity{ci}},
		"developer identity":  {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{ci, dev}},
		"key":                 {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{{PublicKey: "k", TrustLevel: evidence.TrustRunner}}},
		"other repository":    {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{{Issuer: actionsIssuer, SubjectRegexp: `https://github\.com/evil/aval/.*`, TrustLevel: evidence.TrustRunner}}},
		"other issuer":        {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{{Issuer: "https://evil", SubjectRegexp: ci.SubjectRegexp, TrustLevel: evidence.TrustRunner}}},
		"isolated self-grant": {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{{Issuer: actionsIssuer, SubjectRegexp: ci.SubjectRegexp, TrustLevel: evidence.TrustIsolated}}},
	}
	for name, p := range bad {
		if len(mainPolicyProblems(p)) == 0 {
			t.Errorf("main policy with %s: no problem reported", name)
		}
	}
	if p := prPolicyProblems(&policy.Policy{Identities: []evidence.Identity{ci, dev}}, main); len(p) != 0 {
		t.Errorf("PR policy with a developer identity reported %v", p)
	}
	runnerDev := dev
	runnerDev.TrustLevel = evidence.TrustRunner
	if len(prPolicyProblems(&policy.Policy{Identities: []evidence.Identity{ci, runnerDev}}, main)) == 0 {
		t.Error("PR policy granting runner level to a developer: no problem reported")
	}
}

type permJob struct {
	If          string            `yaml:"if"`
	Permissions map[string]string `yaml:"permissions"`
	Steps       []step            `yaml:"steps"`
}

// writeScopeProblems: a job that can publish or sign (packages or id-token
// write) runs only on push, never on pull_request, and runs no test suite,
// so no credential reaches code under test.
func writeScopeProblems(data []byte) ([]string, error) {
	var wf struct {
		Jobs map[string]permJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, err
	}
	var problems []string
	for name, j := range wf.Jobs {
		if j.Permissions["packages"] != "write" && j.Permissions["id-token"] != "write" {
			continue
		}
		if !strings.Contains(j.If, "github.event_name == 'push'") {
			problems = append(problems, name+": write scopes without a push-only condition")
		}
		if slices.ContainsFunc(j.Steps, func(s step) bool {
			return strings.Contains(s.Run, "make ci-e2e") || strings.Contains(s.Run, "aval run --shadow")
		}) {
			problems = append(problems, name+": runs a dogfood suite with write scopes")
		}
	}
	slices.Sort(problems)
	return problems, nil
}

func TestWriteScopedJobsRunOnlyOnPush(t *testing.T) {
	problems, err := writeScopeProblems(readCI(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func TestWriteScopeCheckCatchesPRJobs(t *testing.T) {
	cases := map[string]string{
		"no condition":   "jobs:\n  a:\n    permissions: {id-token: write}\n",
		"on PRs":         "jobs:\n  a:\n    if: github.event_name == 'pull_request'\n    permissions: {packages: write}\n",
		"runs the suite": "jobs:\n  a:\n    if: github.event_name == 'push'\n    permissions: {packages: write}\n    steps: [{run: make ci-e2e}]\n",
	}
	for name, wf := range cases {
		if p, err := writeScopeProblems([]byte(wf)); err != nil || len(p) == 0 {
			t.Errorf("%s: no problem reported (%v)", name, err)
		}
	}
	ok := "jobs:\n  a:\n    if: github.event_name == 'push'\n    permissions: {id-token: write}\n    steps: [{run: aval push}]\n  b:\n    steps: [{run: make ci-e2e}]\n"
	if p, err := writeScopeProblems([]byte(ok)); err != nil || len(p) != 0 {
		t.Errorf("valid workflow reported %v (%v)", p, err)
	}
}
