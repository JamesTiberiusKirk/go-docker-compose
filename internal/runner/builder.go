package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/JamesTiberiusKirk/go-docker-compose/internal/prettyprint"
	"github.com/compose-spec/compose-go/types"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/client"
	"github.com/moby/go-archive"
	"github.com/teris-io/shortid"
)

func buildImage(ctx context.Context, cli *client.Client, service types.ServiceConfig) error {
	// TODO: here we need to append the context to the docker compose file path

	effectiveBuildContextPath := service.Build.Context
	if effectiveBuildContextPath == "" {
		effectiveBuildContextPath = "."
	}

	imageName := service.Image
	if imageName == "" {
		imageName = service.Name
	}

	dockerfilePath := service.Build.Dockerfile

	if service.Build.DockerfileInline != "" {
		tmpContext, dp, err := handleInlineDockerfile(effectiveBuildContextPath, service.Build.DockerfileInline, imageName, service.Name)
		if err != nil {
			return err
		}
		effectiveBuildContextPath = tmpContext
		dockerfilePath = dp
	} else {
		if dockerfilePath == "" {
			dockerfilePath = "Dockerfile"
		}
		fmt.Printf("Building image from Dockerfile '%s' in context '%s' for service: %s\n", dockerfilePath, effectiveBuildContextPath, service.Name)
	}

	if _, err := os.Stat(effectiveBuildContextPath); os.IsNotExist(err) {
		return fmt.Errorf("build context directory '%s' does not exist: %w", effectiveBuildContextPath, err)
	}

	buildCtxReader, err := archive.TarWithOptions(effectiveBuildContextPath, &archive.TarOptions{})
	if err != nil {
		return fmt.Errorf("failed to create tar archive for build context '%s': %w", effectiveBuildContextPath, err)
	}
	defer buildCtxReader.Close()

	dockerBuildArgs := make(map[string]*string)
	if service.Build.Args != nil {
		for k, v := range service.Build.Args {
			val := v
			dockerBuildArgs[k] = val
		}
	}

	buildOptions := build.ImageBuildOptions{
		Tags:       []string{imageName},
		Remove:     true,
		Dockerfile: dockerfilePath,
		BuildArgs:  dockerBuildArgs,
	}

	imageBuildResp, err := cli.ImageBuild(ctx, buildCtxReader, buildOptions)
	if err != nil {
		return fmt.Errorf("Docker image build failed for service %s: %w", service.Name, err)
	}
	defer imageBuildResp.Body.Close()

	if err := prettyprint.PrintDockerStreamProgress(imageBuildResp.Body); err != nil {
		return fmt.Errorf("error from docker image build; service %s failed to build: %w", service.Name, err)
	}

	return nil
}

func handleInlineDockerfile(contextPath, inlineDockerFile, imageName, serviceName string) (string, string, error) {
	sid, err := shortid.Generate()
	if err != nil {
		return "", "", fmt.Errorf("failed to create shortid: %w", err)
	}

	if imageName == "" {
		return "", "", fmt.Errorf("imageName is empty")
	}

	tmpContextPath, err := os.MkdirTemp("", "stackr-build-context-"+imageName+"-"+sid+"-")
	if err != nil {
		return "", "", fmt.Errorf("failed to create temporary build context directory: %w", err)
	}

	err = copyDirectory(contextPath, tmpContextPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to copy build context from '%s' to temporary directory '%s': %w", contextPath, tmpContextPath, err)
	}

	dockerfilePath := "Dockerfile" // Default name for the inline Dockerfile within the temp context
	err = os.WriteFile(filepath.Join(tmpContextPath, dockerfilePath), []byte(inlineDockerFile), 0644)
	if err != nil {
		return "", "", fmt.Errorf("failed to write inline Dockerfile to temporary file '%s': %w", filepath.Join(tmpContextPath, dockerfilePath), err)
	}

	fmt.Printf("Building image from inline Dockerfile for service: %s (using temporary context: %s)\n", serviceName, tmpContextPath)

	return tmpContextPath, dockerfilePath, nil
}

// cleanUpTempDir ensures the temporary directory is removed regardless of build success or failure.
func cleanUpTempDir(tmpContextPath string) {
	if _, statErr := os.Stat(tmpContextPath); statErr == nil || !os.IsNotExist(statErr) {
		os.RemoveAll(tmpContextPath)
	}
}

// copyDirectory recursively copies a directory from src to dst.
func copyDirectory(src string, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		destPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(destPath, info.Mode())
		}

		if path == src && !info.IsDir() {
			return nil
		}

		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()

		dstFile, err := os.Create(destPath)
		if err != nil {
			return err
		}
		defer dstFile.Close()

		_, err = io.Copy(dstFile, srcFile)
		return err
	})
}
