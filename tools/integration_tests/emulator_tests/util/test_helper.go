// Copyright 2024 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package emulator_tests

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"syscall"

	"github.com/googlecloudplatform/gcsfuse/v2/tools/integration_tests/util/setup"
)

// StartProxyServer starts a proxy server as a background process and handles its lifecycle.
//
// It launches the proxy server with the specified configuration and port, logs its output to a file.
func StartProxyServer(configPath string) {
	// Start the proxy in the background
	cmd := exec.Command("go", "run", "../proxy_server/.", "--config-path="+configPath)
	logFileForProxyServer, err := os.Create(path.Join(os.Getenv("KOKORO_ARTIFACTS_DIR"), "proxy-"+setup.GenerateRandomString(5)))
	if err != nil {
		log.Fatal("Error in creating log file for proxy server.")
	}
	log.Printf("Proxy server logs are generated with specific filename %s: ", logFileForProxyServer.Name())
	cmd.Stdout = logFileForProxyServer
	cmd.Stderr = logFileForProxyServer
	err = cmd.Start()
	if err != nil {
		log.Fatal(err)
	}
}

// KillProxyServerProcess kills all processes listening on the specified port.
//
// It uses the `lsof` command to identify the processes and sends SIGINT to each of them.
func KillProxyServerProcess(port int) error {
	// Use lsof to find processes listening on the specified port
	cmd := exec.Command("lsof", "-i", fmt.Sprintf(":%d", port))
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("error running lsof: %w", err)
	}

	// Parse the lsof output to get the process IDs
	lines := strings.Split(string(output), "\n")
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) > 1 {
			pidStr := fields[1]
			pid, err := strconv.Atoi(pidStr)
			if err != nil {
				log.Println("Error parsing process ID:", err)
				continue
			}

			// Send SIGINT to the process
			process, err := os.FindProcess(pid)
			if err != nil {
				log.Println("Error finding process:", err)
				continue
			}
			err = process.Signal(syscall.SIGINT)
			if err != nil {
				log.Println("Error sending SIGINT to process:", err)
			}
		}
	}

	return nil
}
