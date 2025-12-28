package integrationtest

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JamesTiberiusKirk/go-docker-compose/internal/composeconvert"
	"github.com/JamesTiberiusKirk/go-docker-compose/internal/runner"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teris-io/shortid"
)

// NOTE:
// Yes, IK, these tests aren't good, but they serve well for some TDD.
// When I actually start using this and therefore relying on tests to make sure it works, I'll (try to remember) and fix it.
//
// TODO:
// - [ ] Replace the port mappings with dynamic ports which are free (on the host)

func TestCompose(t *testing.T) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)

	tests := []struct {
		name       string
		composeYML string
		assertFunc func(t *testing.T, info container.InspectResponse, sid string)
	}{
		{
			name:       "Environment_variables",
			composeYML: "test_docker_compose/env.yml",
			assertFunc: func(t *testing.T, c container.InspectResponse, sid string) {
				env := c.Config.Env
				assert.Contains(t, env, "FOO=bar")
				assert.Contains(t, env, "HELLO=world")
			},
		},
		{
			name:       "Labels",
			composeYML: "test_docker_compose/labels.yml",
			assertFunc: func(t *testing.T, c container.InspectResponse, sid string) {
				labels := c.Config.Labels
				assert.Equal(t, "true", labels["com.example.test"])
				assert.Equal(t, "v1", labels["version"])
			},
		},
		{
			name:       "Ports",
			composeYML: "test_docker_compose/ports.yml",
			assertFunc: func(t *testing.T, c container.InspectResponse, sid string) {
				bindings, ok := c.HostConfig.PortBindings["80/tcp"]
				require.True(t, ok)

				assert.Contains(t, bindings, nat.PortBinding{HostIP: "", HostPort: "8081"})
			},
		},
		{
			name:       "Volumes",
			composeYML: "test_docker_compose/volumes.yml",
			assertFunc: func(t *testing.T, c container.InspectResponse, sid string) {
				found := false
				for _, m := range c.Mounts {
					if strings.HasSuffix(m.Source, ".docker-mount") &&
						m.Destination == "/usr/share/nginx/html" &&
						m.Type == "bind" {
						found = true
					}
				}
				assert.True(t, found, "volume /usr/share/nginx/html bound from .docker-mount")
			},
		},
		{
			name:       "Hostname",
			composeYML: "test_docker_compose/hostname.yml",
			assertFunc: func(t *testing.T, c container.InspectResponse, sid string) {
				assert.Equal(t, "stackr_test-hostname-test-"+sid, c.Config.Hostname)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()

			sid, err := shortid.Generate()
			require.NoError(t, err)
			sid = strings.ToLower(sid)

			project, err := composeconvert.LoadComposeStack(ctx, composeconvert.LoadComposeProjectOptions{
				NamePrefix:        "stackr_test-",
				NameSuffix:        "-" + sid,
				DockerComposePath: tt.composeYML,
				PullEnvFromSystem: true,
			})
			require.NoError(t, err, "Error from load compose stack")

			t.Cleanup(func() {
				ctx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cleanupCancel()
				for _, svc := range project.Services {

					_, err := cli.ContainerInspect(ctx, svc.Name)
					if err == nil {
						err = cli.ContainerRemove(ctx, svc.Name, container.RemoveOptions{Force: true})
						require.NoError(t, err, "[CLEANUP] Error removing container")
					}

					_, err = cli.ImageInspect(ctx, svc.Image)
					if err == nil {
						_, err = cli.ImageRemove(ctx, svc.Image, image.RemoveOptions{Force: true, PruneChildren: true})
						require.NoError(t, err, "[CLEANUP] Error removing image")
					}

					for _, vol := range svc.Volumes {
						if vol.Type == "bind" && vol.Source != "" {
							err = os.RemoveAll(vol.Source)
							require.NoError(t, err, "[CLEANUP] Error removing bind folder")
							require.NoDirExists(t, vol.Source, "[CLEANUP] Error directory still exists")
						}
					}
				}
			})

			require.NoError(t, runner.Run(ctx, cli, project), "Error running stack")
			time.Sleep(2 * time.Second)

			info, err := cli.ContainerInspect(ctx, project.Services[0].Name)
			require.NoError(t, err, "Error inspecting container")

			tt.assertFunc(t, info, sid)
		})
	}
}
