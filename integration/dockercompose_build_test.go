package integrationtest

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/JamesTiberiusKirk/go-docker-compose/internal/composeconvert"
	"github.com/JamesTiberiusKirk/go-docker-compose/internal/runner"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teris-io/shortid"
)

func TestCompose_Build(t *testing.T) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)

	tests := []struct {
		name       string
		composeYML string
		assertFunc func(t *testing.T, cli *client.Client, info container.InspectResponse, sid string)
	}{
		{
			name:       "Build_from_context",
			composeYML: "test_docker_compose/build/build.yml",
			assertFunc: func(t *testing.T, cli *client.Client, c container.InspectResponse, sid string) {
				assert.Equal(t, "stackr_test-customapp-"+sid, c.Config.Hostname)
				resp, err := http.Get("http://localhost:8082")
				require.NoError(t, err)
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				assert.Contains(t, string(body), "Hello from custom build")
			},
		},
		{
			name:       "Build_from_context_with_custom_image_name",
			composeYML: "test_docker_compose/build/build_with_image.yml",
			assertFunc: func(t *testing.T, cli *client.Client, c container.InspectResponse, sid string) {
				assert.Equal(t, "stackr_test-customapp_customtag-"+sid, c.Config.Hostname)
				assert.Equal(t, "custom_build_image", c.Config.Image)
				resp, err := http.Get("http://localhost:8083")
				require.NoError(t, err)
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				assert.Contains(t, string(body), "Hello from custom build")
			},
		},
		{
			name:       "Build_from_context_with_inline_dockerfile",
			composeYML: "test_docker_compose/build/build_inline_dockerfile.yml",
			assertFunc: func(t *testing.T, cli *client.Client, c container.InspectResponse, sid string) {
				assert.Equal(t, "stackr_test-customapp_inline_dockerfile-"+sid, c.Config.Hostname)
				assert.Equal(t, "stackr_test-customapp_inline_dockerfile-"+sid, c.Config.Image)
				assertContainerLogs(t, cli, c.ID, "testing inline dockerfile")
			},
		},
		{
			name:       "Build_with_build_args",
			composeYML: "test_docker_compose/build/build_args.yml",
			assertFunc: func(t *testing.T, cli *client.Client, c container.InspectResponse, sid string) {
				assert.Equal(t, "stackr_test-app-with-build-arg-"+sid, c.Config.Hostname)
				assert.Equal(t, "custom_build_arg_image", c.Config.Image)
				assertContainerLogs(t, cli, c.ID, "catting file:", "Message from build arg!")
				assertContainerFileContent(t, cli, c.ID, "/message.txt", "Message from build arg!")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			testID, err := shortid.Generate()
			require.NoError(t, err)
			testID = strings.ToLower(testID)

			project, err := composeconvert.LoadComposeStack(ctx, composeconvert.LoadComposeProjectOptions{
				NamePrefix:        "stackr_test-",
				NameSuffix:        "-" + testID,
				DockerComposePath: tt.composeYML,
				PullEnvFromSystem: true,
			})
			require.NoError(t, err, "Error from load compose stack")

			registerProjectCleanup(t, cli, project)

			require.NoError(t, runner.Run(ctx, cli, project), "Error running stack")
			time.Sleep(2 * time.Second)

			info, err := cli.ContainerInspect(ctx, project.Services[0].Name)
			require.NoError(t, err, "Error inspecting container")

			tt.assertFunc(t, cli, info, testID)
		})
	}
}
