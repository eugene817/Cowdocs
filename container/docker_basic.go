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

// StatsRecord holds the final stats for one container run.
type StatsRecord struct {
	ID            string        `json:"id"`
	StartTime     time.Time     `json:"start_time"`
	EndTime       time.Time     `json:"end_time"`
	Duration      time.Duration `json:"duration"`
	CPUUsageNs    uint64        `json:"cpu_usage_ns"`
	MemUsageBytes uint64        `json:"mem_usage_bytes"`
	MemMaxBytes   uint64        `json:"mem_max_bytes"`
}

// cgroupInfo stores the cgroup‐related data we need for a running container.
// We fill this on the "start" event.
type cgroupInfo struct {
	pid      int               // host PID of the container’s init process
	relPaths map[string]string // controller → relative cgroup path (e.g. "/docker/<outer>/docker/<id>")
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

// Monitor listens for Docker "start" and "die" events. As soon as a container dies,
// it uses the pre‐cached cgroupInfo (collected on "start") to read exactly the right files
// under /sys/fs/cgroup and invokes onRecord(rec).
func Monitor(ctx context.Context, onRecord func(rec StatsRecord)) error {
	// 1) New Docker client
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("could not create Docker client: %w", err)
	}
	defer cli.Close()

	// 2) Map containerID → cgroupInfo (pid + relPaths)
	infos := make(map[string]cgroupInfo)

	// 3) Build filters to only get "container start" and "container die"
	filterArgs := filters.NewArgs()
	filterArgs.Add("type", "container")
	filterArgs.Add("event", "start")
	filterArgs.Add("event", "die")

	// 4) Subscribe to events
	messages, errs := cli.Events(ctx, events.ListOptions{Filters: filterArgs})

	for {
		select {
		case err := <-errs:
			return fmt.Errorf("error from Docker events: %w", err)

		case msg := <-messages:
			switch msg.Action {
			case "start":
				// As soon as the container enters "running", capture its PID + cgroup paths
				st := time.Unix(msg.Time, 0)
				pid, relPaths, err := fetchCgroupInfo(ctx, cli, msg.ID)
				if err != nil {
					log.Printf("fetchCgroupInfo(%s) error: %v\n", msg.ID, err)
					// If we fail to fetch cgroup info, we skip storing it.
					// We will not be able to collect metrics on die, but at least the app continues.
					continue
				}
				infos[msg.ID] = cgroupInfo{pid: pid, relPaths: relPaths}
				// Note: we also need to record start time so we can compute duration on "die"
				// We'll overwrite infos[msg.ID] below, so let's keep start time in relPaths under a sentinel key:
				relPaths["__start_time"] = st.Format(time.RFC3339Nano)

			case "die":
				// When container dies, look up its stored cgroupInfo
				info, ok := infos[msg.ID]
				if !ok {
					// We never saw a "start" or failed to fetch info. Skip.
					continue
				}
				// Parse the recorded start time:
				stStr := info.relPaths["__start_time"]
				startTime, err := time.Parse(time.RFC3339Nano, stStr)
				if err != nil {
					// If the stored start time parsing fails, default to msg.Time
					startTime = time.Unix(msg.Time, 0)
				}
				endTime := time.Unix(msg.Time, 0)

				// Now gather CPU + Memory using the cached cgroupInfo
				rec, err := readStatsFromCgroup(info, msg.ID, startTime, endTime)
				if err != nil {
					log.Printf("error reading stats for %s: %v\n", msg.ID, err)
					delete(infos, msg.ID)
					continue
				}

				// Invoke the callback
				onRecord(*rec)

				// Cleanup
				delete(infos, msg.ID)
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// fetchCgroupInfo inspects a container to find its host PID, then reads /proc/<pid>/cgroup
// to build a map: controller → relative cgroup path. This is called on the "start" event.
//
// Returns (pid, relPaths, error).
func fetchCgroupInfo(ctx context.Context, cli *client.Client, containerID string) (int, map[string]string, error) {
	insp, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return 0, nil, fmt.Errorf("ContainerInspect(%s) failed: %w", containerID, err)
	}
	pid := insp.State.Pid
	if pid <= 0 {
		return 0, nil, fmt.Errorf("invalid PID %d for container %s", pid, containerID)
	}

	// Read /proc/<pid>/cgroup
	cgFile := fmt.Sprintf("/proc/%d/cgroup", pid)
	f, err := os.Open(cgFile)
	if err != nil {
		return pid, nil, fmt.Errorf("could not open %s: %w", cgFile, err)
	}
	defer f.Close()

	relPaths := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
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
		return pid, relPaths, fmt.Errorf("scanner error on %s: %w", cgFile, err)
	}

	return pid, relPaths, nil
}

// readStatsFromCgroup actually reads the CPU and memory files under /sys/fs/cgroup.
// It takes the cached cgroupInfo (pid + relPaths) we built on "start", plus start/end times.
func readStatsFromCgroup(info cgroupInfo, containerID string, start, end time.Time) (*StatsRecord, error) {
	relPaths := info.relPaths

	// Helper to build a v1 path:
	buildV1 := func(controller, filename string) (string, error) {
		p, ok := relPaths[controller]
		if !ok {
			return "", fmt.Errorf("controller %s not found for container %s", controller, containerID)
		}
		return filepath.Join("/sys/fs/cgroup", controller, p, filename), nil
	}

	var (
		cpuUsageNs    uint64
		memUsageBytes uint64
		memMaxBytes   uint64
	)

	// Detect cgroup v2 if relPaths[""] is set
	if unifiedPath, isV2 := relPaths[""]; isV2 {
		// cgroup v2
		//  - CPU:   /sys/fs/cgroup/<unifiedPath>/cpu.stat  (parse usage_usec)
		//  - Memory:/sys/fs/cgroup/<unifiedPath>/memory.current, memory.peak
		cpuStatPath := filepath.Join("/sys/fs/cgroup", unifiedPath, "cpu.stat")
		data, err := ioutil.ReadFile(cpuStatPath)
		if err != nil {
			return nil, fmt.Errorf("could not read %s: %w", cpuStatPath, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "usage_usec") {
				var usec uint64
				fmt.Sscanf(line, "usage_usec %d", &usec)
				cpuUsageNs = usec * 1000 // µs → ns
				break
			}
		}

		curPath := filepath.Join("/sys/fs/cgroup", unifiedPath, "memory.current")
		d1, err := ioutil.ReadFile(curPath)
		if err != nil {
			return nil, fmt.Errorf("read memory.current: %w", err)
		}
		fmt.Sscanf(string(d1), "%d", &memUsageBytes)

		peakPath := filepath.Join("/sys/fs/cgroup", unifiedPath, "memory.peak")
		d2, err := ioutil.ReadFile(peakPath)
		if err != nil {
			return nil, fmt.Errorf("read memory.peak: %w", err)
		}
		fmt.Sscanf(string(d2), "%d", &memMaxBytes)
	} else {
		// cgroup v1
		cpuPath, err := buildV1("cpu,cpuacct", "cpuacct.usage")
		if err != nil {
			return nil, err
		}
		d0, err := ioutil.ReadFile(cpuPath)
		if err != nil {
			return nil, fmt.Errorf("read cpuacct.usage: %w", err)
		}
		fmt.Sscanf(string(d0), "%d", &cpuUsageNs)

		muPath, err := buildV1("memory", "memory.usage_in_bytes")
		if err != nil {
			return nil, err
		}
		d1, err := ioutil.ReadFile(muPath)
		if err != nil {
			return nil, fmt.Errorf("read memory.usage_in_bytes: %w", err)
		}
		fmt.Sscanf(string(d1), "%d", &memUsageBytes)

		mpPath, err := buildV1("memory", "memory.max_usage_in_bytes")
		if err != nil {
			return nil, err
		}
		d2, err := ioutil.ReadFile(mpPath)
		if err != nil {
			return nil, fmt.Errorf("read memory.max_usage_in_bytes: %w", err)
		}
		fmt.Sscanf(string(d2), "%d", &memMaxBytes)
	}

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
