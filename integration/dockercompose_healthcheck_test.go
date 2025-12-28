package integrationtest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JamesTiberiusKirk/go-docker-compose/internal/composeconvert"
	"github.com/JamesTiberiusKirk/go-docker-compose/internal/runner"
	"github.com/compose-spec/compose-go/types"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teris-io/shortid"
)

func TestCompose_Healthcheck(t *testing.T) {
	// t.Skip()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)

	type runAssert func(t *testing.T, cli *client.Client, project *types.Project)

	tests := []struct {
		name       string
		composeYML string
		assertRun  func(t *testing.T, cli *client.Client, project *types.Project)
	}{
		{
			name:       "Healthy_status",
			composeYML: "test_docker_compose/healthcheck/healthy.yml",
			assertRun: func(t *testing.T, cli *client.Client, project *types.Project) {
				svc := findServiceName(project, "hc-ok")
				require.NotEmpty(t, svc)

				waitHealthStatus(t, cli, svc, "healthy", 20*time.Second)

				info, err := cli.ContainerInspect(t.Context(), svc)
				require.NoError(t, err)
				assert.True(t, info.State.Running)
				require.NotNil(t, info.State.Health)
				assert.Equal(t, "healthy", info.State.Health.Status)
			},
		},
		{
			name:       "Unhealthy_status",
			composeYML: "test_docker_compose/healthcheck/unhealthy.yml",
			assertRun: func(t *testing.T, cli *client.Client, project *types.Project) {
				svc := findServiceName(project, "hc-bad")
				require.NotEmpty(t, svc)

				waitHealthStatus(t, cli, svc, "unhealthy", 20*time.Second)

				info, err := cli.ContainerInspect(t.Context(), svc)
				require.NoError(t, err)
				require.NotNil(t, info.State.Health)
				assert.Equal(t, "unhealthy", info.State.Health.Status)
			},
		},
		{
			name:       "No_healthcheck_present",
			composeYML: "test_docker_compose/healthcheck/no_healthcheck.yml",
			assertRun: func(t *testing.T, cli *client.Client, project *types.Project) {
				svc := findServiceName(project, "hc-none")
				require.NotEmpty(t, svc)

				info, err := cli.ContainerInspect(t.Context(), svc)
				require.NoError(t, err)
				assert.Nil(t, info.State.Health)
				assert.True(t, info.State.Running)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Note: needed external context so it properly cleans it up
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			sid, err := shortid.Generate()
			require.NoError(t, err)
			sid = strings.ToLower(sid)

			project, err := composeconvert.LoadComposeStack(ctx, composeconvert.LoadComposeProjectOptions{
				NamePrefix:        "stackr_test-",
				NameSuffix:        "-" + sid,
				DockerComposePath: tt.composeYML,
				PullEnvFromSystem: false,
			})
			require.NoError(t, err)

			registerProjectCleanup(t, cli, project)

			require.NoError(t, runner.Run(ctx, cli, project))
			time.Sleep(2 * time.Second)

			tt.assertRun(t, cli, project)
		})
	}
}

// minimal poller
func waitHealthStatus(t *testing.T, cli *client.Client, name, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := cli.ContainerInspect(t.Context(), name)
		if err == nil && info.State != nil && info.State.Health != nil {
			if info.State.Health.Status == want {
				return
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s to become %s", name, want)
}
