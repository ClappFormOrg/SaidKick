package handlers

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/config"
)

// requiredValueRE matches `required "..." .Values.<key>` in a chart template.
var requiredValueRE = regexp.MustCompile(`required\s+"[^"]*"\s+\.Values\.([A-Za-z0-9_.]+)`)

// agentChartRequiredKeys returns every value the agent chart marks required.
func agentChartRequiredKeys(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "charts", "saidkick-agent", "templates", "*.yaml"))
	if err != nil {
		t.Fatalf("glob agent templates: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no agent chart templates found")
	}
	seen := map[string]bool{}
	var keys []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range requiredValueRE.FindAllStringSubmatch(string(b), -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				keys = append(keys, m[1])
			}
		}
	}
	if len(keys) == 0 {
		t.Fatal("no required values found in agent chart templates")
	}
	return keys
}

func TestAgentInstallCommand_SetsEveryRequiredChartValue(t *testing.T) {
	t.Parallel()
	cmd := agentInstallCommand(agentInstall{
		ChartRepo:    "oci://ghcr.io/clappformorg/charts",
		ChartVersion: "0.3.0",
		HubURL:       "https://appstore.example.com",
		HubAgentURL:  "https://agents.example.com:8443",
		ClusterID:    "0190c3a4-0000-7000-8000-000000000001",
		JoinToken:    "jwt.token.value",
		VaultMount:   "secret",
	})

	for _, key := range append(agentChartRequiredKeys(t), "agent.joinToken", "vault.mount") {
		if !strings.Contains(cmd, "--set "+key+"=") {
			t.Errorf("command missing --set %s=\n%s", key, cmd)
		}
	}
	for _, want := range []string{
		"--set agent.hubUrl=https://appstore.example.com",
		"--set agent.hubAgentUrl=https://agents.example.com:8443",
		"--set agent.clusterID=0190c3a4-0000-7000-8000-000000000001",
		"--set agent.joinToken=jwt.token.value",
		"--set vault.mount=secret",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command missing %q\n%s", want, cmd)
		}
	}
	first, _, _ := strings.Cut(cmd, " \\\n")
	if want := "helm install saidkick-agent oci://ghcr.io/clappformorg/charts/saidkick-agent --version 0.3.0"; first != want {
		t.Errorf("first line = %q, want %q", first, want)
	}
	if strings.Contains(cmd, "--repo") {
		t.Errorf("an OCI chart reference takes no --repo\n%s", cmd)
	}
}

func TestAgentInstallCommand_ChartReference(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, repo, version, want string
	}{
		{"default repo, no version", "", "", "helm install saidkick-agent " + config.DefaultAgentChartRepo + "/saidkick-agent"},
		{"default repo with version", "", "0.3.0", "helm install saidkick-agent oci://ghcr.io/clappformorg/charts/saidkick-agent --version 0.3.0"},
		{"configured repo, trailing slash", "oci://ghcr.io/example/charts/", "1.0.0", "helm install saidkick-agent oci://ghcr.io/example/charts/saidkick-agent --version 1.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := agentInstallCommand(agentInstall{ChartRepo: tc.repo, ChartVersion: tc.version, ClusterID: "id", JoinToken: "tok"})
			first, _, _ := strings.Cut(cmd, " \\\n")
			if first != tc.want {
				t.Errorf("first line = %q, want %q", first, tc.want)
			}
		})
	}
}

func TestHandlers_AgentInstallFor(t *testing.T) {
	t.Parallel()
	h := &Handlers{
		HubEndpoint:       "https://appstore.example.com",
		HubAgentEndpoint:  "https://agents.example.com:8443",
		VaultMount:        "secret",
		AgentChartRepo:    "oci://ghcr.io/clappformorg/charts",
		AgentChartVersion: "0.3.0",
	}
	want := agentInstall{
		ChartRepo:    "oci://ghcr.io/clappformorg/charts",
		ChartVersion: "0.3.0",
		HubURL:       "https://appstore.example.com",
		HubAgentURL:  "https://agents.example.com:8443",
		ClusterID:    "id",
		JoinToken:    "tok",
		VaultMount:   "secret",
	}
	if got := h.agentInstallFor("id", "tok"); got != want {
		t.Errorf("agentInstallFor = %+v, want %+v", got, want)
	}
}

// setFlagRE matches the key of each --set flag in an install command.
var setFlagRE = regexp.MustCompile(`--set ([A-Za-z0-9_.]+)=`)

func TestAgentInstallCommand_MatchesChartDocs(t *testing.T) {
	t.Parallel()
	cmd := agentInstallCommand(agentInstall{
		ChartVersion: "0.3.0",
		HubURL:       "https://appstore.example.com",
		HubAgentURL:  "https://agents.example.com:8443",
		ClusterID:    "id",
		JoinToken:    "tok",
		VaultMount:   "kv",
	})
	chartRef := "helm install saidkick-agent " + config.DefaultAgentChartRepo + "/saidkick-agent --version "
	chartDir := filepath.Join("..", "..", "..", "charts", "saidkick-agent")
	for _, doc := range []string{"README.md", "values.yaml"} {
		b, err := os.ReadFile(filepath.Join(chartDir, doc))
		if err != nil {
			t.Fatalf("read %s: %v", doc, err)
		}
		text := string(b)
		if !strings.Contains(text, chartRef) {
			t.Errorf("%s does not show the hub's chart reference %q", doc, chartRef)
		}
		for _, m := range setFlagRE.FindAllStringSubmatch(cmd, -1) {
			if !strings.Contains(text, "--set "+m[1]+"=") {
				t.Errorf("%s install command is missing --set %s=", doc, m[1])
			}
		}
	}
}

func TestClusterInstaller_StepsInstallESOBeforeTheAgent(t *testing.T) {
	t.Parallel()
	agentCmd := agentInstallCommand(agentInstall{HubURL: "https://appstore.example.com", ClusterID: "id", JoinToken: "tok"})
	inst := clusterInstaller(agentCmd, "tok")

	steps, ok := inst["steps"].([]map[string]string)
	if !ok || len(steps) != 2 {
		t.Fatalf("steps = %#v, want two ordered steps", inst["steps"])
	}
	if steps[0]["name"] != "external-secrets" || steps[1]["name"] != "saidkick-agent" {
		t.Errorf("step order = %q, %q; want external-secrets, saidkick-agent", steps[0]["name"], steps[1]["name"])
	}
	eso := steps[0]["command"]
	for _, want := range []string{
		"helm install external-secrets external-secrets --repo https://charts.external-secrets.io",
		"--version " + esoChartVersion,
		"--create-namespace",
		"--wait",
	} {
		if !strings.Contains(eso, want) {
			t.Errorf("ESO step missing %q\n%s", want, eso)
		}
	}
	if steps[1]["command"] != agentCmd {
		t.Errorf("agent step = %q, want the agent install command", steps[1]["command"])
	}
	if inst["helm_command"] != agentCmd {
		t.Errorf("helm_command = %q, want the agent step for older clients", inst["helm_command"])
	}
	if inst["join_token_jwt"] != "tok" {
		t.Errorf("join_token_jwt = %q, want tok", inst["join_token_jwt"])
	}
}

// TestESOInstallCommand_MatchesChartDocs keeps the ESO version the hub prints
// in step with the prerequisite the agent chart documents.
func TestESOInstallCommand_MatchesChartDocs(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "charts", "saidkick-agent", "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	// A Windows checkout may carry CRLF line endings.
	if !strings.Contains(strings.ReplaceAll(string(b), "\r\n", "\n"), esoInstallCommand()) {
		t.Errorf("charts/saidkick-agent/README.md does not show the ESO install command the hub prints:\n%s", esoInstallCommand())
	}
}

func TestAgentInstallCommand_Defaults(t *testing.T) {
	t.Parallel()
	cmd := agentInstallCommand(agentInstall{ClusterID: "id", JoinToken: "tok"})
	if !strings.Contains(cmd, `--set agent.hubUrl="$HUB_URL"`) {
		t.Errorf("empty hub URL should become a placeholder\n%s", cmd)
	}
	if !strings.Contains(cmd, "--set vault.mount=kv") {
		t.Errorf("empty vault mount should default to kv\n%s", cmd)
	}
	if strings.Contains(cmd, "agent.hubAgentUrl") {
		t.Errorf("an unset agent listener URL should be left out so the agent falls back to hubUrl\n%s", cmd)
	}
}
