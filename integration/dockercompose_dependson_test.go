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

func TestCompose_DependsOn(t *testing.T) {
	// t.Skip()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)

	type runAssert func(t *testing.T, cli *client.Client, project *types.Project, sid string)

	tests := []struct {
		name                 string
		composeYML           string
		assertComposeOrder   func(t *testing.T, testID string, project *types.Project)
		assertRunAfterLaunch runAssert
	}{
		{
			name:       "Basic_order_test",
			composeYML: "test_docker_compose/dependson/basic_order.yml",
			assertComposeOrder: func(t *testing.T, _ string, project *types.Project) {
				require.Len(t, project.Services, 2)
				assert.True(t, strings.Contains(project.Services[0].Name, "srv2"))
				assert.True(t, strings.Contains(project.Services[1].Name, "srv1"))
			},
		},
		{
			name:       "Condition_service_healthy_enforces_start_after_health",
			composeYML: "test_docker_compose/dependson/healthy.yml",
			assertRunAfterLaunch: func(t *testing.T, cli *client.Client, project *types.Project, _ string) {
				dbName := findServiceName(project, "db")
				require.NotEmpty(t, dbName)

				apiName := findServiceName(project, "api")
				require.NotEmpty(t, apiName)

				// Wait until DB reports healthy.
				waitHealthStatus(t, cli, dbName, "healthy", 25*time.Second)

				dbInfo, err := cli.ContainerInspect(t.Context(), dbName)
				require.NoError(t, err)
				require.NotNil(t, dbInfo.State)
				require.NotNil(t, dbInfo.State.Health)
				assert.Equal(t, "healthy", dbInfo.State.Health.Status)

				// Time of first successful healthcheck.
				var healthyAt time.Time
				for _, h := range dbInfo.State.Health.Log {
					if h != nil && h.ExitCode == 0 && h.End.After(healthyAt) {
						healthyAt = h.End
					}
				}
				require.False(t, healthyAt.IsZero(), "no successful healthcheck found")

				apiInfo, err := cli.ContainerInspect(t.Context(), apiName)
				require.NoError(t, err)
				require.NotNil(t, apiInfo.State)
				assert.True(t, apiInfo.State.Running, "api should be running")

				apiStarted := mustParseDockerTime(t, apiInfo.State.StartedAt)

				// Core assertion: dependent started AFTER dependency became healthy.
				assert.Truef(t, apiStarted.After(healthyAt) || apiStarted.Equal(healthyAt),
					"api started at %v, but db became healthy at %v", apiStarted, healthyAt)

				// Sanity: app printed success path.
				assertContainerLogs(t, cli, apiInfo.ID, "API_OK")
			},
		},
		{
			name:       "Condition_service_completed_successfully_enforces_start_after_exit0",
			composeYML: "test_docker_compose/dependson/completed.yml",
			assertRunAfterLaunch: func(t *testing.T, cli *client.Client, project *types.Project, _ string) {
				migName := findServiceName(project, "migrate")
				require.NotEmpty(t, migName)

				appName := findServiceName(project, "app")
				require.NotEmpty(t, appName)

				// Give migrate time to finish.
				deadline := time.Now().Add(25 * time.Second)
				for time.Now().Before(deadline) {
					migInfo, _ := cli.ContainerInspect(context.Background(), migName)
					if migInfo.State != nil && !migInfo.State.Running {
						break
					}
					time.Sleep(200 * time.Millisecond)
				}

				migInfo, err := cli.ContainerInspect(t.Context(), migName)
				require.NoError(t, err)
				require.NotNil(t, migInfo.State)
				assert.False(t, migInfo.State.Running, "migrate should have exited")
				assert.Equal(t, 0, migInfo.State.ExitCode, "migrate should exit 0")

				appInfo, err := cli.ContainerInspect(t.Context(), appName)
				require.NoError(t, err)
				require.NotNil(t, appInfo.State)
				assert.True(t, appInfo.State.Running, "app should be running")

				migFinished := mustParseDockerTime(t, migInfo.State.FinishedAt)
				appStarted := mustParseDockerTime(t, appInfo.State.StartedAt)

				// Core assertion: dependent started AFTER migrate finished successfully.
				assert.Truef(t, appStarted.After(migFinished) || appStarted.Equal(migFinished),
					"app started at %v, but migrate finished at %v", appStarted, migFinished)

				assertContainerLogs(t, cli, appInfo.ID, "APP_OK")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
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
			require.NoError(t, err, "Error from load compose stack")

			registerProjectCleanup(t, cli, project)

			if tt.assertRunAfterLaunch != nil {
				require.NoError(t, runner.Run(ctx, cli, project), "Error running stack")
				time.Sleep(1 * time.Second)
				tt.assertRunAfterLaunch(t, cli, project, sid)
			} else if tt.assertComposeOrder != nil {
				tt.assertComposeOrder(t, sid, project)
			}
		})
	}
}
