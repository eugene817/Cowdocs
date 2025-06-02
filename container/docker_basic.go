package container

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// DockerManager struct with cli field
type DockerManager struct {
	cli *client.Client
}

// ContainerStatsSummary holds the aggregated stats we want at container exit.
type ContainerStatsSummary struct {
	Duration       time.Duration // Total time the container ran
	LastMemUsage   uint64        // Memory usage in the last stats frame (bytes)
	PeakMemUsage   uint64        // Peak memory usage over the container’s lifetime (bytes)
	LastCPUPercent float64       // CPU percentage in the last stats frame
}

// Docker manager constructor
func NewDockerManager() (*DockerManager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &DockerManager{cli: cli}, nil
}

// ensureImage checks if there is an image of the container
// if not it pulls it.
func (dm *DockerManager) EnsureImage(imageName string) error {
	ctx := context.Background()

	// Inspect the image to check if it exists
	if _, _, err := dm.cli.ImageInspectWithRaw(ctx, imageName); err == nil {
		return nil // Image exists, no need to pull
	}

	// pull
	reader, err := dm.cli.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageName, err)
	}
	defer reader.Close()
	// close the stream
	if _, err := io.Copy(os.Stdout, reader); err != nil {
		return fmt.Errorf("failed to read pull response for %s: %w", imageName, err)
	}
	return nil
}

// Function to create a container
func (dm *DockerManager) Create(config ContainerConfig) (string, error) {

	// Ensure the image is available
	if err := dm.EnsureImage(config.Image); err != nil {
		return "", err
	}

	ctx := context.Background()
	containerConfig := &container.Config{
		Image: config.Image,
		Cmd:   config.Cmd,
		Tty:   config.Tty,
	}
	resp, err := dm.cli.ContainerCreate(ctx, containerConfig, nil, nil, nil, "")
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// Function to start a container
func (dm *DockerManager) Start(containerID string) error {
	ctx := context.Background()
	if err := dm.cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start container: %v", err)
	}
	return nil
}

// Function to stop a container
func (dm *DockerManager) Stop(containerID string, timeout int) error {
	ctx := context.Background()
	if err := dm.cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &[]int{timeout}[0]}); err != nil {
		return fmt.Errorf("failed to stop container: %v", err)
	}
	return nil
}

// Function to remove a container
func (dm *DockerManager) Remove(containerID string) error {
	ctx := context.Background()
	if err := dm.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("failed to remove container: %v", err)
	}
	return nil
}

// Function to wait for a container
func (dm *DockerManager) Wait(containerID string) (container.WaitResponse, error) {
	ctx := context.Background()
	respC, errC := dm.cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)

	select {
	case resp := <-respC:
		return resp, nil
	case err := <-errC:
		return container.WaitResponse{}, fmt.Errorf("failed to wait for container: %w", err)
	}
}

// Function to check if a container is running
func (dm *DockerManager) IsRunning(containerID string) (bool, error) {
	ctx := context.Background()
	inspect, err := dm.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return false, fmt.Errorf("failed to inspect container: %v", err)
	}
	return inspect.State.Running, nil
}

// Function to get logs from a container
func (dm *DockerManager) GetLogs(containerID string) (string, error) {
	ctx := context.Background()
	logsReader, err := dm.cli.ContainerLogs(ctx, containerID, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return "", fmt.Errorf("failed to get logs: %v", err)
	}
	defer logsReader.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, logsReader); err != nil {
		return "", fmt.Errorf("failed to read logs: %v", err)
	}

	if stderr.Len() > 0 {
		return "", fmt.Errorf("error in logs: %s", stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}

// Function to get stats from docker container and format them
func (dm *DockerManager) GetStats(containerID string) (string, error) {
	ctx := context.Background()
	stats, err := dm.cli.ContainerStatsOneShot(ctx, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to get stats: %v", err)
	}

	statsBytes, err := io.ReadAll(stats.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read stats: %v", err)
	}
	defer stats.Body.Close()

	var statsData container.StatsResponse
	if err := json.Unmarshal(statsBytes, &statsData); err != nil {
		return "", fmt.Errorf("failed to parse stats: %v", err)
	}

	statsFormatted, err := json.MarshalIndent(statsData, "", " ")
	if err != nil {
		return "", fmt.Errorf("failed to format stats: %v", err)
	}

	return string(statsFormatted), nil
}

func (dm *DockerManager) Ping() error {
	ctx := context.Background()
	_, err := dm.cli.Ping(ctx)
	return err
}

func (dm *DockerManager) GetStatsStreamed(
	containerID string,
	startTime time.Time,
	resultCh chan<- ContainerStatsSummary,
	errCh chan<- error,
) {
	ctx := context.Background()

	// 1. Open streaming stats (stream=true)
	resp, err := dm.cli.ContainerStats(ctx, containerID, true)
	if err != nil {
		errCh <- fmt.Errorf("failed to open stats stream: %v", err)
		return
	}
	defer resp.Body.Close()

	decoder := json.NewDecoder(resp.Body)

	// 2. Decode frames into container.StatsResponse (not types.StatsJSON)
	var (
		frame   container.StatsResponse
		peakMem uint64 = 0
		lastMem uint64 = 0
		lastCPU float64
	)

	// 3. Loop until EOF (container exit)
	for {
		if err := decoder.Decode(&frame); err != nil {
			if err == io.EOF {
				// streaming closed because container exited
				break
			}
			errCh <- fmt.Errorf("error decoding stats frame: %v", err)
			return
		}

		// 4. Update peak memory and last memory
		used := frame.MemoryStats.Usage
		if used > peakMem {
			peakMem = used
		}
		lastMem = used

		// 5. Compute CPU% using Docker’s formula:
		cpuDelta := float64(frame.CPUStats.CPUUsage.TotalUsage) -
			float64(frame.PreCPUStats.CPUUsage.TotalUsage)
		systemDelta := float64(frame.CPUStats.SystemUsage) -
			float64(frame.PreCPUStats.SystemUsage)
		cpuPercent := 0.0
		if systemDelta > 0 && cpuDelta > 0 {
			cpuPercent = (cpuDelta / systemDelta) *
				float64(len(frame.CPUStats.CPUUsage.PercpuUsage)) * 100.0
		}
		lastCPU = cpuPercent
	}

	// 6. Once EOF is reached, the container has exited; compute duration
	duration := time.Since(startTime)

	// 7. Send summary on resultCh
	resultCh <- ContainerStatsSummary{
		Duration:       duration,
		LastMemUsage:   lastMem,
		PeakMemUsage:   peakMem,
		LastCPUPercent: lastCPU,
	}
}

// StatsRecord must match (or be convertible from) container.ContainerStatsSummary
type StatsRecord struct {
	ID            string        `json:"id"`
	StartTime     time.Time     `json:"start_time"`
	EndTime       time.Time     `json:"end_time"`
	Duration      time.Duration `json:"duration"`
	CPUUsageNs    uint64        `json:"cpu_usage_ns"`
	MemUsageBytes uint64        `json:"mem_usage_bytes"`
	MemMaxBytes   uint64        `json:"mem_max_bytes"`
}

// Monitor is a helper you add into your application (not in the library).
// As soon as a container dies, it calls onRecord(...) with a StatsRecord.
func Monitor(ctx context.Context, onRecord func(rec StatsRecord)) error {
	// Create a Docker client (same as in Cowdocs)
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("could not create Docker client: %w", err)
	}
	defer cli.Close()

	// Keep a map from containerID → time it started
	startTimes := make(map[string]time.Time)

	// Build filters.Args to get only container start/die events
	filterArgs := filters.NewArgs()
	filterArgs.Add("type", "container")
	filterArgs.Add("event", "start")
	filterArgs.Add("event", "die")

	// Pass filterArgs into events.ListOptions
	messages, errs := cli.Events(ctx, events.ListOptions{Filters: filterArgs})
	for {
		select {
		case err := <-errs:
			return fmt.Errorf("error from Docker events: %w", err)

		case msg := <-messages:
			// msg.Action is "start" or "die"
			// msg.Time is int64 (Unix seconds)
			switch msg.Action {
			case "start":
				startTimes[msg.ID] = time.Unix(msg.Time, 0)

			case "die":
				st, ok := startTimes[msg.ID]
				if !ok {
					// If we never saw "start", skip it
					continue
				}
				endTime := time.Unix(msg.Time, 0)

				// Read cgroup files as soon as the container dies
				rec, err := captureMetrics(msg.ID, st, endTime)
				if err != nil {
					// Just log and move on
					fmt.Fprintf(os.Stderr, "captureMetrics(%s) error: %v\n", msg.ID, err)
					delete(startTimes, msg.ID)
					continue
				}
				onRecord(*rec)
				delete(startTimes, msg.ID)
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// captureMetrics reads exactly the same cgroup‐based CPU/memory from DockerManager.GetStatsStreamed
// but in this function we read them directly from /sys/fs/cgroup because we know DockerManager.GetStatsStreamed
// already implements streaming. If you want to re-use DockerManager.GetStatsStreamed, you can do so here.
func captureMetrics(containerID string, start, end time.Time) (*StatsRecord, error) {
	base := "/sys/fs/cgroup"
	cpuPath := filepath.Join(base, "cpu,cpuacct/docker", containerID, "cpuacct.usage")
	memUsagePath := filepath.Join(base, "memory/docker", containerID, "memory.usage_in_bytes")
	memMaxPath := filepath.Join(base, "memory/docker", containerID, "memory.max_usage_in_bytes")

	cpuBytes, err := ioutil.ReadFile(cpuPath)
	if err != nil {
		return nil, fmt.Errorf("read cpu usage: %w", err)
	}
	var cpuUsageNs uint64
	fmt.Sscanf(string(cpuBytes), "%d", &cpuUsageNs)

	memBytes, err := ioutil.ReadFile(memUsagePath)
	if err != nil {
		return nil, fmt.Errorf("read mem usage: %w", err)
	}
	var memUsage uint64
	fmt.Sscanf(string(memBytes), "%d", &memUsage)

	memMaxBytes, err := ioutil.ReadFile(memMaxPath)
	if err != nil {
		return nil, fmt.Errorf("read mem max usage: %w", err)
	}
	var memMax uint64
	fmt.Sscanf(string(memMaxBytes), "%d", &memMax)

	return &StatsRecord{
		ID:            containerID,
		StartTime:     start,
		EndTime:       end,
		Duration:      end.Sub(start),
		CPUUsageNs:    cpuUsageNs,
		MemUsageBytes: memUsage,
		MemMaxBytes:   memMax,
	}, nil
}
