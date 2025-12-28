package composeconvert

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/loader"
	"github.com/compose-spec/compose-go/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/go-connections/nat"
	"gopkg.in/yaml.v3"
)

type LoadComposeProjectOptions struct {
	DockerComposePath string
	NamePrefix        string
	NameSuffix        string
	// This will overwrite any existing env pulled from system (if its enabled)
	Env               map[string]string
	PullEnvFromSystem bool
	WorkingDir        string
}

func LoadComposeStack(ctx context.Context, ops LoadComposeProjectOptions) (*types.Project, error) {
	file, err := os.Open(ops.DockerComposePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open compose file: %w", err)
	}
	defer file.Close()

	var raw map[string]any
	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to decode YAML: %w", err)
	}

	env := map[string]string{}
	if ops.PullEnvFromSystem {
		for _, e := range os.Environ() {
			parts := strings.SplitN(e, "=", 2)
			if len(parts) == 2 {
				env[parts[0]] = parts[1]
			}
		}
	}
	maps.Copy(env, ops.Env)

	workingDir := ops.WorkingDir
	if workingDir == "" {
		pwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get pwd: %w", err)
		}
		workingDir = pwd
	}

	composeDir := filepath.Dir(ops.DockerComposePath)

	project, err := loader.Load(types.ConfigDetails{
		WorkingDir: composeDir,
		ConfigFiles: []types.ConfigFile{
			{Filename: ops.DockerComposePath, Config: raw},
		},
		Environment: env,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to load compose project: %w", err)
	}

	// 1) Sort services BEFORE renaming so DependsOn keys still match original names.
	orderedServices, err := topoSortServices(project.Services)
	if err != nil {
		return nil, fmt.Errorf("failed to order services: %w", err)
	}

	// 2) Apply name prefix/suffix AND rewrite depends_on keys to the new names.
	if ops.NamePrefix != "" || ops.NameSuffix != "" {
		nameMap := make(map[string]string) // old -> new
		for i := range orderedServices {
			old := orderedServices[i].Name
			newName := old
			if ops.NamePrefix != "" {
				newName = ops.NamePrefix + newName
			}
			if ops.NameSuffix != "" {
				newName = newName + ops.NameSuffix
			}
			nameMap[old] = newName
		}
		for i := range orderedServices {
			// rename service
			orderedServices[i].Name = nameMap[orderedServices[i].Name]
			// rewrite depends_on keys
			if len(orderedServices[i].DependsOn) > 0 {
				m := make(map[string]types.ServiceDependency, len(orderedServices[i].DependsOn))
				for k, v := range orderedServices[i].DependsOn {
					if nk, ok := nameMap[k]; ok {
						m[nk] = v
					} else {
						m[k] = v
					}
				}
				orderedServices[i].DependsOn = m
			}
		}
	}

	project.Services = orderedServices
	return project, nil
}

func TranslateServiceConfigToContainerConfig(service types.ServiceConfig) (*container.Config, *container.HostConfig, *network.NetworkingConfig, error) {
	envVars := []string{}
	for key, val := range service.Environment {
		envVars = append(envVars, fmt.Sprintf("%s=%s", key, *val))
	}

	config := &container.Config{
		Image:    service.Image,
		Env:      envVars,
		Cmd:      strslice.StrSlice(service.Command),
		Labels:   service.Labels,
		Hostname: service.Name,
	}

	hostConfig := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{
			Name: container.RestartPolicyMode(service.Restart),
		},
		Binds: []string{},
	}

	for _, vol := range service.Volumes {
		if vol.Source != "" && vol.Target != "" {
			hostConfig.Binds = append(hostConfig.Binds, fmt.Sprintf("%s:%s", vol.Source, vol.Target))
		}
	}

	// Healthcheck
	if service.HealthCheck != nil {
		hc := service.HealthCheck
		dhc := &container.HealthConfig{}

		// test (e.g. ["CMD","test","-f","/tmp/ready"])
		if len(hc.Test) > 0 {
			var test []string
			for _, s := range hc.Test {
				test = append(test, s)
			}
			dhc.Test = test
		}
		// durations
		if hc.Interval != nil {
			dhc.Interval = time.Duration(*hc.Interval)
		}
		if hc.Timeout != nil {
			dhc.Timeout = time.Duration(*hc.Timeout)
		}
		if hc.StartPeriod != nil {
			dhc.StartPeriod = time.Duration(*hc.StartPeriod)
		}
		if hc.Retries != nil {
			dhc.Retries = int(*hc.Retries)
		}
		config.Healthcheck = dhc
	}

	portBindings := nat.PortMap{}
	exposedPorts := nat.PortSet{}

	for _, port := range service.Ports {
		protocol := string(port.Protocol)
		if protocol == "" {
			protocol = "tcp"
		}

		portKey := nat.Port(fmt.Sprintf("%d/%s", port.Target, protocol))
		exposedPorts[portKey] = struct{}{}

		if port.Published != "" {
			publishedPort, err := strconv.Atoi(port.Published)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("invalid published port %q: %w", port.Published, err)
			}

			binding := nat.PortBinding{
				HostPort: fmt.Sprintf("%d", publishedPort),
				HostIP:   port.HostIP,
			}
			portBindings[portKey] = append(portBindings[portKey], binding)
		}
	}

	config.ExposedPorts = exposedPorts
	hostConfig.PortBindings = portBindings
	networkConfig := &network.NetworkingConfig{}

	return config, hostConfig, networkConfig, nil
}

func topoSortServices(services []types.ServiceConfig) ([]types.ServiceConfig, error) {
	graph := map[string][]string{}
	inDegree := map[string]int{}
	serviceMap := map[string]types.ServiceConfig{}

	for _, svc := range services {
		serviceMap[svc.Name] = svc
		inDegree[svc.Name] = 0
	}

	for _, svc := range services {
		for dep := range svc.DependsOn {
			graph[dep] = append(graph[dep], svc.Name)
			inDegree[svc.Name]++
		}
	}

	var queue []string
	for name, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, name)
		}
	}
	sort.Strings(queue)

	var result []types.ServiceConfig
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		result = append(result, serviceMap[curr])

		for _, neighbor := range graph[curr] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				queue = append(queue, neighbor)
				sort.Strings(queue)
			}
		}
	}

	if len(result) != len(services) {
		return nil, fmt.Errorf("circular or unresolved dependencies detected")
	}

	return result, nil
}
