// +build integration

// Copyright 2019 Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//	http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package engine

import (
	"testing"
	"time"

	apicontainer "github.com/aws/amazon-ecs-agent/agent/api/container"
	"github.com/stretchr/testify/assert"
)

const restartingTimeout = 90 * time.Second

// TestAutoRestartOnFailure is a happy-case integration test that ensure container is restarted.
// TODO: need to be updated after auto-restart CP changes
func TestAutoRestartOnFailure(t *testing.T) {
	taskEngine, done, _ := setupWithDefaultConfig(t)
	defer done()

	stateChangeEvents := taskEngine.StateChangeEvents()

	taskArn := "testAutoRestartOnFailure"
	testTask := createTestTask(taskArn)

	container1 := createTestContainerWithImageAndName(baseImageForOS, "container1")
	container2 := createTestContainerWithImageAndName(baseImageForOS, "container2")

	container1.EntryPoint = &entryPointForOS
	container1.Command = []string{"sleep 15"}
	container1.Essential = true

	var restartMaxAttempts apicontainer.RestartCount = 3
	container2.EntryPoint = &entryPointForOS
	container2.Command = []string{"sleep 2 && exit 1"}
	container2.Essential = false
	container2.RestartPolicy = apicontainer.OnFailure
	container2.RestartMaxAttempts = restartMaxAttempts

	testTask.Containers = []*apicontainer.Container{
		container1,
		container2,
	}

	go taskEngine.AddTask(testTask)

	finished := make(chan interface{})
	go func() {
		// Both containers should start
		verifyContainerRunningStateChange(t, taskEngine)
		verifyContainerRunningStateChange(t, taskEngine)
		verifyTaskIsRunning(stateChangeEvents, testTask)

		// Status changes monitored
		// TODO: change to verify `Restarting` and `Running` change after CP changes
		for i := 0; i < int(restartMaxAttempts); i++ {
			verifyContainerRunningStateChange(t, taskEngine)
		}

		// After exhausted all retries, container2 is stopped
		verifyContainerStoppedStateChange(t, taskEngine)
		verifyContainerStoppedStateChange(t, taskEngine)

		assert.True(t, container2.GetRestartAttempts() > 0, "Did not restarted")
		assert.Equal(t, restartMaxAttempts, container2.GetRestartAttempts(), "Did not exhaust all restart attempts")

		verifyTaskIsStopped(stateChangeEvents, testTask)
		close(finished)
	}()

	waitFinished(t, finished, restartingTimeout)
}