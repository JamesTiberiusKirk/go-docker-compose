package integrationtest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/require"
)

// assertContainerLogs reads container logs and asserts that all expectedMessages are present.
func assertContainerLogs(t *testing.T, cli *client.Client, cid string, expectedMessages ...string) {
	t.Helper()
	logReader, err := cli.ContainerLogs(t.Context(), cid, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     false,
	})
	require.NoError(t, err, "Failed to get container logs")
	defer logReader.Close()

	reader := bufio.NewReader(logReader)
	var allLogs strings.Builder
	messagesFound := make([]bool, len(expectedMessages))

	for {
		header := make([]byte, 8)
		_, err := io.ReadFull(reader, header)
		if err == io.EOF {
			break
		}
		require.NoError(t, err, "Error reading Docker log header")

		count := binary.BigEndian.Uint32(header[4:])
		if count == 0 {
			continue
		}

		line := make([]byte, count)
		_, err = io.ReadFull(reader, line)
		require.NoError(t, err, "Error reading Docker log content")

		logLine := string(line)
		t.Logf("DOCKER LOG: %q", logLine)
		allLogs.WriteString(logLine)

		for i, msg := range expectedMessages {
			if !messagesFound[i] && strings.Contains(logLine, msg) {
				messagesFound[i] = true
			}
		}
	}

	t.Logf("Full collected logs:\n%s", allLogs.String())

	for i, found := range messagesFound {
		require.Truef(t, found, "Container logs do not contain the expected message: %q", expectedMessages[i])
	}
}

// assertContainerFileContent checks that the file at containerPath inside the container with ID cid
// contains the expectedContent substring.
func assertContainerFileContent(t *testing.T, cli *client.Client, cid, containerPath, expectedContent string) {
	t.Helper()
	reader, _, err := cli.CopyFromContainer(t.Context(), cid, containerPath)
	require.NoError(t, err, "Failed to copy file from container")
	defer reader.Close()

	var buf bytes.Buffer
	// CopyFromContainer returns a tar archive. We need to extract the file contents:
	tr := bufio.NewReader(reader)
	_, err = io.Copy(&buf, tr)
	require.NoError(t, err, "Failed to read file content from container")

	content := buf.String()
	require.Contains(t, content, expectedContent, "Container file %q does not contain expected content", containerPath)
}

func findServiceName(project *types.Project, name string) string {
	var svc string
	for _, s := range project.Services {
		if strings.Contains(s.Name, name) {
			svc = s.Name
		}
	}

	return svc
}

func mustParseDockerTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, s)
	require.NoError(t, err)
	return ts
}

func registerProjectCleanup(t *testing.T, cli *client.Client, project *types.Project) {
	t.Helper()

	// Snapshot pre-existing bind paths so we don't delete repo fixtures.
	preExisting := map[string]bool{}
	for _, svc := range project.Services {
		for _, vol := range svc.Volumes {
			if vol.Type == "bind" && vol.Source != "" {
				if _, err := os.Stat(vol.Source); err == nil {
					preExisting[vol.Source] = true
				}
			}
		}
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()

		for _, svc := range project.Services {
			// remove exact-name container if present
			if _, err := cli.ContainerInspect(ctx, svc.Name); err == nil {
				_ = cli.ContainerRemove(ctx, svc.Name, container.RemoveOptions{
					Force:         true,
					RemoveVolumes: true,
				})
			}
			// sweep any strays still matching the name
			args := filters.NewArgs()
			args.Add("name", svc.Name)
			if list, _ := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: args}); len(list) > 0 {
				for _, c := range list {
					_ = cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{
						Force:         true,
						RemoveVolumes: true,
					})
				}
			}

			// remove image if present
			if svc.Image != "" {
				if _, err := cli.ImageInspect(ctx, svc.Image); err == nil {
					_, _ = cli.ImageRemove(ctx, svc.Image, image.RemoveOptions{
						Force:         true,
						PruneChildren: true,
					})
				}
			}

			// remove bind dirs only if the test created them
			for _, vol := range svc.Volumes {
				if vol.Type == "bind" && vol.Source != "" && !preExisting[vol.Source] {
					_ = os.RemoveAll(vol.Source)
					require.NoDirExists(t, vol.Source, "[CLEANUP] directory still exists: %s", vol.Source)
				}
			}
		}
	})
}
