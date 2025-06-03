package container

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
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

// Monitor listens for Docker "start" and "die" events. When a container dies,
// it calls onRecord(StatsRecord) with the metrics just collected.
func Monitor(ctx context.Context, onRecord func(rec StatsRecord)) error {
	// 1) Create a Docker client
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("could not create Docker client: %w", err)
	}
	defer cli.Close()

	// 2) Keep a map containerID → start time
	startTimes := make(map[string]time.Time)

	// 3) Build filters to only receive "container start" and "container die" events
	filterArgs := filters.NewArgs()
	filterArgs.Add("type", "container")
	filterArgs.Add("event", "start")
	filterArgs.Add("event", "die")

	// 4) Subscribe to events (use events.ListOptions with filterArgs)
	messages, errs := cli.Events(ctx, events.ListOptions{Filters: filterArgs})

	for {
		select {
		case err := <-errs:
			return fmt.Errorf("error from Docker events: %w", err)

		case msg := <-messages:
			// msg.Action is "start" or "die"; msg.Time is int64 UNIX seconds
			switch msg.Action {
			case "start":
				// Record the start timestamp
				startTimes[msg.ID] = time.Unix(msg.Time, 0)

			case "die":
				// Container has stopped
				st, ok := startTimes[msg.ID]
				if !ok {
					// Missed “start” event? Skip.
					continue
				}
				endTime := time.Unix(msg.Time, 0)

				// Gather cgroup metrics dynamically
				rec, err := captureMetrics(ctx, cli, msg.ID, st, endTime)
				if err != nil {
					// Log the error, but continue handling other events
					log.Printf("captureMetrics(%s) error: %v\n", msg.ID, err)
					delete(startTimes, msg.ID)
					continue
				}
				// Invoke the user-supplied callback
				onRecord(*rec)

				delete(startTimes, msg.ID)
			}
		case <-ctx.Done():
			return nil
		}
	}
}
func captureMetrics(
	ctx context.Context,
	cli *client.Client,
	containerID string,
	start, end time.Time,
) (*StatsRecord, error) {
	// 1) Inspect container to find its PID in the host namespace
	insp, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("ContainerInspect(%s) failed: %w", containerID, err)
	}
	pid := insp.State.Pid
	if pid <= 0 {
		return nil, fmt.Errorf("invalid PID %d for container %s", pid, containerID)
	}

	// 2) Read /proc/<pid>/cgroup to get relative cgroup paths under each controller
	cgFile := fmt.Sprintf("/proc/%d/cgroup", pid)
	f, err := os.Open(cgFile)
	if err != nil {
		return nil, fmt.Errorf("could not open %s: %w", cgFile, err)
	}
	defer f.Close()

	// relPaths maps “controller” → relative cgroup path (e.g. “/docker/<outer>/docker/<id>”)
	relPaths := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Format: “<hierarchyID>:<controllers>:<relativePath>”
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		controllers := strings.Split(parts[1], ",")
		path := parts[2]
		for _, ctrl := range controllers {
			relPaths[ctrl] = path
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading %s: %w", cgFile, err)
	}

	// Helper: builds a full cgroup v1 path, given a controller and a filename.
	buildV1Path := func(controller, filename string) (string, error) {
		rp, ok := relPaths[controller]
		if !ok {
			return "", fmt.Errorf("controller %s not found for PID %d", controller, pid)
		}
		return filepath.Join("/sys/fs/cgroup", controller, rp, filename), nil
	}

	var (
		cpuUsageNs    uint64
		memUsageBytes uint64
		memMaxBytes   uint64
	)

	// 3) Determine whether we’re on cgroup v2 (unified) or v1.
	//    In v2, relPaths[""] will be non‐empty (empty controller field).
	if unifiedPath, isV2 := relPaths[""]; isV2 {
		// **cgroup v2 logic**:
		//   - CPU usage: /sys/fs/cgroup/<unifiedPath>/cpu.stat → “usage_usec <n>”
		//   - Memory usage: /sys/fs/cgroup/<unifiedPath>/memory.current
		//   - Memory peak:  /sys/fs/cgroup/<unifiedPath>/memory.peak

		// 3a) CPU (read cpu.stat → usage_usec → convert to nanoseconds)
		cpuStat := filepath.Join("/sys/fs/cgroup", unifiedPath, "cpu.stat")
		data, err := ioutil.ReadFile(cpuStat)
		if err != nil {
			return nil, fmt.Errorf("could not read %s: %w", cpuStat, err)
		}
		for _, ln := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(ln, "usage_usec") {
				var usec uint64
				fmt.Sscanf(ln, "usage_usec %d", &usec)
				cpuUsageNs = usec * 1000 // µs → ns
				break
			}
		}

		// 3b) Current memory usage
		memCur := filepath.Join("/sys/fs/cgroup", unifiedPath, "memory.current")
		d1, err := ioutil.ReadFile(memCur)
		if err != nil {
			return nil, fmt.Errorf("could not read %s: %w", memCur, err)
		}
		fmt.Sscanf(string(d1), "%d", &memUsageBytes)

		// 3c) Peak memory usage
		memPeak := filepath.Join("/sys/fs/cgroup", unifiedPath, "memory.peak")
		d2, err := ioutil.ReadFile(memPeak)
		if err != nil {
			return nil, fmt.Errorf("could not read %s: %w", memPeak, err)
		}
		fmt.Sscanf(string(d2), "%d", &memMaxBytes)
	} else {
		// **cgroup v1 logic**:
		//   - CPU:    /sys/fs/cgroup/cpu,cpuacct/<relPaths["cpu,cpuacct"]>/cpuacct.usage
		//   - Memory: /sys/fs/cgroup/memory/<relPaths["memory"]>/memory.usage_in_bytes
		//             /sys/fs/cgroup/memory/<relPaths["memory"]>/memory.max_usage_in_bytes

		// 3d) CPU usage v1
		cpuPath, err := buildV1Path("cpu,cpuacct", "cpuacct.usage")
		if err != nil {
			return nil, err
		}
		d0, err := ioutil.ReadFile(cpuPath)
		if err != nil {
			return nil, fmt.Errorf("could not read %s: %w", cpuPath, err)
		}
		fmt.Sscanf(string(d0), "%d", &cpuUsageNs)

		// 3e) Memory usage v1
		muPath, err := buildV1Path("memory", "memory.usage_in_bytes")
		if err != nil {
			return nil, err
		}
		d1, err := ioutil.ReadFile(muPath)
		if err != nil {
			return nil, fmt.Errorf("could not read %s: %w", muPath, err)
		}
		fmt.Sscanf(string(d1), "%d", &memUsageBytes)

		// 3f) Memory peak v1
		mpPath, err := buildV1Path("memory", "memory.max_usage_in_bytes")
		if err != nil {
			return nil, err
		}
		d2, err := ioutil.ReadFile(mpPath)
		if err != nil {
			return nil, fmt.Errorf("could not read %s: %w", mpPath, err)
		}
		fmt.Sscanf(string(d2), "%d", &memMaxBytes)
	}

	// 4) Build and return StatsRecord
	return &StatsRecord{
		ID:            containerID,
		StartTime:     start,
		EndTime:       end,
		Duration:      end.Sub(start),
		CPUUsageNs:    cpuUsageNs,
		MemUsageBytes: memUsageBytes,
		MemMaxBytes:   memMaxBytes,
	}, nil
}
