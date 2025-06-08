package api

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/eugene817/Cowdocs/container"
)

// API struct with containerManager field
type API struct {
	containerManager container.Manager
}

// Function to create a new API instance
func NewAPI(mgr container.Manager) *API {
	return &API{containerManager: mgr}
}

// StatsResponse — минимальный набор полей из Docker‐статистики, которые нам нужны.
type StatsResponse struct {
	CPUStats struct {
		// TotalUsage — кумулятивные наносекунды CPU
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		// SystemUsage — кумулятивные наносекунды всех ядер
		SystemUsage uint64 `json:"system_cpu_usage"`
	} `json:"cpu_stats"`

	// PreCPUStats нужен, если ты захочешь рассчитывать %-ные изменения по docker-алгоритму
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`

	MemoryStats struct {
		Usage uint64 `json:"usage"`
		Limit uint64 `json:"limit"`
	} `json:"memory_stats"`
}

// RunContainer создаёт, запускает контейнер, стримит stats, ждёт финиша и возвращает логи + JSON-метрики.
func (api *API) RunContainer(cfg container.ContainerConfig, showStats bool) (string, string, error) {
	// 1) Create + defer Remove
	id, err := api.containerManager.Create(cfg)
	if err != nil {
		return "", "", fmt.Errorf("create container: %w", err)
	}
	defer api.containerManager.Remove(id)

	// 2) Start + засечь старт
	start := time.Now()
	if err := api.containerManager.Start(id); err != nil {
		return "", "", fmt.Errorf("start container: %w", err)
	}

	// Переменные для стрима
	var (
		firstUsage uint64
		lastUsage  uint64
		firstSet   bool
		streamErr  error
		wg         sync.WaitGroup
	)

	if showStats {
		statsRdr, err := api.containerManager.StreamStats(id)
		if err != nil {
			return "", "", fmt.Errorf("open stats stream: %w", err)
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer statsRdr.Close()

			dec := json.NewDecoder(statsRdr)
			for {
				var sr container.StatsResponse
				if err := dec.Decode(&sr); err != nil {
					if err == io.EOF {
						return
					}
					streamErr = err
					return
				}
				// Монотонный CPU-счётчик
				usage := sr.CPUStats.CPUUsage.TotalUsage
				if !firstSet {
					firstUsage = usage
					firstSet = true
				}
				lastUsage = usage
			}
		}()
	}

	// 3) Ждём exit
	if _, err := api.containerManager.Wait(id); err != nil {
		return "", "", fmt.Errorf("wait container: %w", err)
	}
	// Дождаться потока
	if showStats {
		wg.Wait()
		if streamErr != nil {
			return "", "", fmt.Errorf("stats stream error: %w", streamErr)
		}
	}

	// 4) Логи
	logs, err := api.containerManager.GetLogs(id)
	if err != nil {
		return "", "", fmt.Errorf("get logs: %w", err)
	}

	// 5) Формируем JSON-ответ по stats
	var statsJSON string
	if showStats {
		end := time.Now()
		duration := end.Sub(start)

		// Безопасная дельта CPU
		var deltaNs uint64
		if lastUsage >= firstUsage {
			deltaNs = lastUsage - firstUsage
		} else {
			// на случай переполнения счётчика
			deltaNs = lastUsage
		}

		cpuPercent := float64(deltaNs) / float64(duration.Nanoseconds()) * 100

		// Для памяти можно взять последний кадр sr.MemoryStats,
		// поэтому нужно захватывать его аналогично CPU,
		// но в простейшем варианте — оставить 0 или убрать измерения.
		//
		// Если вам нужна финальная память,
		// захватывайте и её в потоке в переменной lastMemUsage/lastMemLimit.

		summary := container.ContainerStatsSummary{
			DurationStr: duration.String(),
			CPUUsageNs:  deltaNs,
			CPUPercent:  cpuPercent,
			// здесь пока нули, или возьмите из вашего sr
			MemUsageKB: 0,
			MemLimitKB: 0,
			MemPercent: 0,
		}

		b, _ := json.MarshalIndent(summary, "", "  ")
		statsJSON = string(b)
	}

	return logs, statsJSON, nil
}

// Function to run containers in parallel using goroutines and channels.
// The container logs are sent to the channel.
// Creates. Starts, Waits and Removes the container.
func (api *API) RunContainerParallel(config container.ContainerConfig, wg *sync.WaitGroup, c chan string) error {
	defer wg.Done()
	id, err := api.containerManager.Create(config)
	if err != nil {
		return fmt.Errorf("failed to create container: %v", err)
	}
	defer api.containerManager.Remove(id)

	if err := api.containerManager.Start(id); err != nil {
		return fmt.Errorf("failed to start container: %v", err)
	}

	if _, err := api.containerManager.Wait(id); err != nil {
		return fmt.Errorf("failed to wait for container: %v", err)
	}

	logs, err := api.containerManager.GetLogs(id)
	if err != nil {
		return fmt.Errorf("failed to get logs: %v", err)
	}
	c <- logs
	return nil
}

func (api *API) EnsureImages(images []string) error {
	for _, image := range images {
		if err := api.containerManager.EnsureImage(image); err != nil {
			return fmt.Errorf("failed to ensure image %s: %v", image, err)
		}
	}
	return nil
}

func (api *API) Ping() error {
	return api.containerManager.Ping()
}
