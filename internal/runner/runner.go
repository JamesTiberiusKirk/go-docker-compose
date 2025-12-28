// internal/runner/runner.go
package runner

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/JamesTiberiusKirk/go-docker-compose/internal/composeconvert"
	"github.com/compose-spec/compose-go/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

func waitForCondition(ctx context.Context, cli *client.Client, name, cond, targetHealth string) error {
	if cond == "" || cond == "service_started" {
		return nil
	}
	if targetHealth == "" {
		targetHealth = "healthy"
	}

	check := func() (bool, error) {
		info, err := cli.ContainerInspect(ctx, name)
		if err != nil || info.State == nil {
			return false, nil
		}
		switch cond {
		case "service_healthy":
			if info.State.Health != nil && info.State.Health.Status == targetHealth {
				return true, nil
			}
			return false, nil
		case "service_completed_successfully":
			if !info.State.Running {
				if info.State.ExitCode == 0 {
					return true, nil
				}
				return false, fmt.Errorf("%s exited with code %d", name, info.State.ExitCode)
			}
			return false, nil
		default:
			return true, nil
		}
	}

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		done, err := check()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for %s (%s): %w", name, cond, ctx.Err())
		case <-ticker.C:
		}
	}
}

func Run(ctx context.Context, cli *client.Client, stackConfig *types.Project) error {
	for i := range stackConfig.Services {
		service := stackConfig.Services[i]
		fmt.Printf("\nPreparing service: %s\n", service.Name)

		// wait for depends_on (keys already rewritten in composeconvert)
		for depName, dep := range service.DependsOn {
			if err := waitForCondition(ctx, cli, depName, string(dep.Condition), "healthy"); err != nil {
				return fmt.Errorf("waiting on dependency %s for service %s: %w", depName, service.Name, err)
			}
		}

		if service.Build != nil {
			if err := buildImage(ctx, cli, service); err != nil {
				return fmt.Errorf("error building new image for service %s: %w", service.Name, err)
			}
			if service.Image == "" {
				service.Image = service.Name
				stackConfig.Services[i].Image = service.Image
			}
			fmt.Printf("Built image: %s\n", service.Image)
		} else {
			fmt.Printf("Pulling image: %s\n", service.Image)
			reader, err := cli.ImagePull(ctx, service.Image, image.PullOptions{})
			if err != nil {
				return fmt.Errorf("pull image %s: %w", service.Image, err)
			}
			io.Copy(io.Discard, reader)
			reader.Close()
		}

		config, hostConfig, netConfig, err := composeconvert.TranslateServiceConfigToContainerConfig(service)
		if err != nil {
			return fmt.Errorf("translate service %s config: %w", service.Name, err)
		}

		resp, err := cli.ContainerCreate(ctx, config, hostConfig, netConfig, nil, service.Name)
		if err != nil {
			return fmt.Errorf("create container %s: %w", service.Name, err)
		}

		fmt.Printf("Starting container %s (ID: %s)\n", service.Name, resp.ID[:12])
		if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
			return fmt.Errorf("start container %s (ID: %s): %w", service.Name, resp.ID[:12], err)
		}
	}
	return nil
}
