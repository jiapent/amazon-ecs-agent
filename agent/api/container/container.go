// Copyright 2014-2018 Amazon.com, Inc. or its affiliates. All Rights Reserved.
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

package container

import (
	"fmt"
	"github.com/aws/amazon-ecs-agent/agent/utils/retry"
	"math"
	"strconv"
	"sync"
	"time"

	apicontainerstatus "github.com/aws/amazon-ecs-agent/agent/api/container/status"
	apierrors "github.com/aws/amazon-ecs-agent/agent/api/errors"
	"github.com/aws/amazon-ecs-agent/agent/credentials"
	resourcestatus "github.com/aws/amazon-ecs-agent/agent/taskresource/status"
	"github.com/aws/aws-sdk-go/aws"

	"github.com/docker/docker/api/types"
)

const (
	// defaultContainerSteadyStateStatus defines the container status at
	// which the container is assumed to be in steady state. It is set
	// to 'ContainerRunning' unless overridden
	defaultContainerSteadyStateStatus = apicontainerstatus.ContainerRunning

	// awslogsAuthExecutionRole is the string value passed in the task payload
	// that specifies that the log driver should be authenticated using the
	// execution role
	awslogsAuthExecutionRole = "ExecutionRole"

	// DockerHealthCheckType is the type of container health check provided by docker
	DockerHealthCheckType = "docker"

	// AuthTypeECR is to use image pull auth over ECR
	AuthTypeECR = "ecr"

	// AuthTypeASM is to use image pull auth over AWS Secrets Manager
	AuthTypeASM = "asm"

	// MetadataURIEnvironmentVariableName defines the name of the environment
	// variable in containers' config, which can be used by the containers to access the
	// v3 metadata endpoint
	MetadataURIEnvironmentVariableName = "ECS_CONTAINER_METADATA_URI"
	// MetadataURIFormat defines the URI format for v3 metadata endpoint
	MetadataURIFormat = "http://169.254.170.2/v3/%s"

	// SecretProviderSSM is to show secret provider being SSM
	SecretProviderSSM = "ssm"

	// SecretProviderASM is to show secret provider being ASM
	SecretProviderASM = "asm"

	// SecretTypeEnv is to show secret type being ENVIRONMENT_VARIABLE
	SecretTypeEnv = "ENVIRONMENT_VARIABLE"

	// TargetLogDriver is to show secret target being "LOG_DRIVER", the default will be "CONTAINER"
	SecretTargetLogDriver = "LOG_DRIVER"

	// Default RestartMaxAttempts OnFailure
	DefaultRestartMaxAttemptsOnFailure = math.MaxInt32
)

const (
	// not restarting at all
	Never RestartPolicy = iota
	// restarts a container if exit code is non-zero
	OnFailure
	// always restart container regardless of exit code and max retries unless the task is stopped
	UnlessTaskStopped
)

type RestartPolicy int32

type RestartCount int32

// DockerConfig represents additional metadata about a container to run. It's
// remodeled from the `ecsacs` api model file. Eventually it should not exist
// once this remodeling is refactored out.
type DockerConfig struct {
	// Config is the configuration used to create container
	Config *string `json:"config"`
	// HostConfig is the configuration of container related to host resource
	HostConfig *string `json:"hostConfig"`
	// Version specifies the docker client API version to use
	Version *string `json:"version"`
}

// HealthStatus contains the health check result returned by docker
type HealthStatus struct {
	// Status is the container health status
	Status apicontainerstatus.ContainerHealthStatus `json:"status,omitempty"`
	// Since is the timestamp when container health status changed
	Since *time.Time `json:"statusSince,omitempty"`
	// ExitCode is the exitcode of health check if failed
	ExitCode int `json:"exitCode,omitempty"`
	// Output is the output of health check
	Output string `json:"output,omitempty"`
}

type RestartInfo struct {
	// RestartPolicy define in what condition will container be restarted
	RestartPolicy RestartPolicy
	// max restart retries if restarts 'On-Failure'
	RestartMaxAttempts RestartCount
	// current retries used
	RestartAttempts RestartCount
	// auto restart exponential backoff
	RestartBackoff *retry.ExponentialBackoff

	// DesiredToRestartWhenReceivingStopped is true if we need to restart container due to api call(`Docker container start`) error
	// This is  used when ever "Docker Start" has an error, in this situation we should restart the container not considering the exit code.
	DesiredToRestartWhenReceivingStopped bool

	// DesiredToFullyStopWhenReceivingStopped is true if we are forcing a container to stop due to error, so won't try to restart.
	// This field is used in 2 situations:
	// 1. Other Docker api calls have error, e.g. "Docker Create" has an error and we are not trying to restart it.
	// 2. The task is stopped and every restarting container should not try to restart but drive to stop.
	//
	// Typically, `DesiredToRestartWhenReceivingStopped` and `DesiredToFullyStopWhenReceivingStopped` are both false
	// if no error occurs calling Docker apis and the task is not desired to stop.
	//
	// An example of these two fields both being true: A container has error starting it, and we set
	// `DesiredToRestartWhenReceivingStopped` to be true, and call Docker api to stop it. Before we receiver the "Stopped" response,
	// an essential of the same task may be stopped, and this container's DesiredToFullyStopWhenReceivingStopped is set to be true.
	// When the "Stopped" message come back, we are not trying to restart it.
	DesiredToFullyStopWhenReceivingStopped bool
}

// Container is the internal representation of a container in the ECS agent
type Container struct {
	// Name is the name of the container specified in the task definition
	Name string
	// RuntimeID is the docker id of the container
	RuntimeID string
	// DependsOn is the field which specifies the ordering for container startup and shutdown.
	DependsOn []DependsOn `json:"dependsOn,omitempty"`
	// V3EndpointID is a container identifier used to construct v3 metadata endpoint; it's unique among
	// all the containers managed by the agent
	V3EndpointID string
	// Image is the image name specified in the task definition
	Image string
	// ImageID is the local ID of the image used in the container
	ImageID string
	// Command is the command to run in the container which is specified in the task definition
	Command []string
	// CPU is the cpu limitation of the container which is specified in the task definition
	CPU uint `json:"Cpu"`
	// GPUIDs is the list of GPU ids for a container
	GPUIDs []string
	// Memory is the memory limitation of the container which is specified in the task definition
	Memory uint
	// Links contains a list of containers to link, corresponding to docker option: --link
	Links []string
	// VolumesFrom contains a list of container's volume to use, corresponding to docker option: --volumes-from
	VolumesFrom []VolumeFrom `json:"volumesFrom"`
	// MountPoints contains a list of volume mount paths
	MountPoints []MountPoint `json:"mountPoints"`
	// Ports contains a list of ports binding configuration
	Ports []PortBinding `json:"portMappings"`
	// Secrets contains a list of secret
	Secrets []Secret `json:"secrets"`
	// Essential denotes whether the container is essential or not
	Essential bool
	// Restart information for container
	RestartInfo *RestartInfo

	// EntryPoint is entrypoint of the container, corresponding to docker option: --entrypoint
	EntryPoint *[]string
	// Environment is the environment variable set in the container
	Environment map[string]string `json:"environment"`
	// Overrides contains the configuration to override of a container
	Overrides ContainerOverrides `json:"overrides"`
	// DockerConfig is the configuration used to create the container
	DockerConfig DockerConfig `json:"dockerConfig"`
	// RegistryAuthentication is the auth data used to pull image
	RegistryAuthentication *RegistryAuthenticationData `json:"registryAuthentication"`
	// HealthCheckType is the mechanism to use for the container health check
	// currently it only supports 'DOCKER'
	HealthCheckType string `json:"healthCheckType,omitempty"`
	// Health contains the health check information of container health check
	Health HealthStatus `json:"-"`
	// LogsAuthStrategy specifies how the logs driver for the container will be
	// authenticated
	LogsAuthStrategy string
	// StartTimeout specifies the time value after which if a container has a dependency
	// on another container and the dependency conditions are 'SUCCESS', 'COMPLETE', 'HEALTHY',
	// then that dependency will not be resolved.
	StartTimeout uint
	// StopTimeout specifies the time value to be passed as StopContainer api call
	StopTimeout uint

	// lock is used for fields that are accessed and updated concurrently
	lock sync.RWMutex

	// DesiredStatusUnsafe represents the state where the container should go. Generally,
	// the desired status is informed by the ECS backend as a result of either
	// API calls made to ECS or decisions made by the ECS service scheduler,
	// though the agent may also set the DesiredStatusUnsafe if a different "essential"
	// container in the task exits. The DesiredStatus is almost always either
	// ContainerRunning or ContainerStopped.
	// NOTE: Do not access DesiredStatusUnsafe directly.  Instead, use `GetDesiredStatus`
	// and `SetDesiredStatus`.
	// TODO DesiredStatusUnsafe should probably be private with appropriately written
	// setter/getter.  When this is done, we need to ensure that the UnmarshalJSON
	// is handled properly so that the state storage continues to work.
	DesiredStatusUnsafe apicontainerstatus.ContainerStatus `json:"desiredStatus"`

	// KnownStatusUnsafe represents the state where the container is.
	// NOTE: Do not access `KnownStatusUnsafe` directly.  Instead, use `GetKnownStatus`
	// and `SetKnownStatus`.
	// TODO KnownStatusUnsafe should probably be private with appropriately written
	// setter/getter.  When this is done, we need to ensure that the UnmarshalJSON
	// is handled properly so that the state storage continues to work.
	KnownStatusUnsafe apicontainerstatus.ContainerStatus `json:"KnownStatus"`

	// TransitionDependenciesMap is a map of the dependent container status to other
	// dependencies that must be satisfied in order for this container to transition.
	TransitionDependenciesMap TransitionDependenciesMap `json:"TransitionDependencySet"`

	// SteadyStateDependencies is a list of containers that must be in "steady state" before
	// this one is created
	// Note: Current logic requires that the containers specified here are run
	// before this container can even be pulled.
	//
	// Deprecated: Use TransitionDependencySet instead. SteadyStateDependencies is retained for compatibility with old
	// state files.
	SteadyStateDependencies []string `json:"RunDependencies"`

	// Type specifies the container type. Except the 'Normal' type, all other types
	// are not directly specified by task definitions, but created by the agent. The
	// JSON tag is retained as this field's previous name 'IsInternal' for maintaining
	// backwards compatibility. Please see JSON parsing hooks for this type for more
	// details
	Type ContainerType `json:"IsInternal"`

	// AppliedStatus is the status that has been "applied" (e.g., we've called Pull,
	// Create, Start, or Stop) but we don't yet know that the application was successful.
	// No need to save it in the state file, as agent will synchronize the container status
	// on restart and for some operation eg: pull, it has to be recalled again.
	AppliedStatus apicontainerstatus.ContainerStatus `json:"-"`
	// ApplyingError is an error that occurred trying to transition the container
	// to its desired state. It is propagated to the backend in the form
	// 'Name: ErrorString' as the 'reason' field.
	ApplyingError *apierrors.DefaultNamedError

	// SentStatusUnsafe represents the last KnownStatusUnsafe that was sent to the ECS
	// SubmitContainerStateChange API.
	// TODO SentStatusUnsafe should probably be private with appropriately written
	// setter/getter.  When this is done, we need to ensure that the UnmarshalJSON is
	// handled properly so that the state storage continues to work.
	SentStatusUnsafe apicontainerstatus.ContainerStatus `json:"SentStatus"`

	// MetadataFileUpdated is set to true when we have completed updating the
	// metadata file
	MetadataFileUpdated bool `json:"metadataFileUpdated"`

	// KnownExitCodeUnsafe specifies the exit code for the container.
	// It is exposed outside of the package so that it's marshalled/unmarshalled in
	// the JSON body while saving the state.
	// NOTE: Do not access KnownExitCodeUnsafe directly. Instead, use `GetKnownExitCode`
	// and `SetKnownExitCode`.
	KnownExitCodeUnsafe *int `json:"KnownExitCode"`

	// KnownPortBindingsUnsafe is an array of port bindings for the container.
	KnownPortBindingsUnsafe []PortBinding `json:"KnownPortBindings"`

	// VolumesUnsafe is an array of volume mounts in the container.
	VolumesUnsafe []types.MountPoint `json:"-"`

	// NetworkModeUnsafe is the network mode in which the container is started
	NetworkModeUnsafe string `json:"-"`

	// NetworksUnsafe denotes the Docker Network Settings in the container.
	NetworkSettingsUnsafe *types.NetworkSettings `json:"-"`

	// SteadyStateStatusUnsafe specifies the steady state status for the container
	// If uninitialized, it's assumed to be set to 'ContainerRunning'. Even though
	// it's not only supposed to be set when the container is being created, it's
	// exposed outside of the package so that it's marshalled/unmarshalled in the
	// the JSON body while saving the state
	SteadyStateStatusUnsafe *apicontainerstatus.ContainerStatus `json:"SteadyStateStatus,omitempty"`

	createdAt  time.Time
	startedAt  time.Time
	finishedAt time.Time

	labels map[string]string
}

type DependsOn struct {
	ContainerName string `json:"containerName"`
	Condition     string `json:"condition"`
}

// DockerContainer is a mapping between containers-as-docker-knows-them and
// containers-as-we-know-them.
// This is primarily used in DockerState, but lives here such that tasks and
// containers know how to convert themselves into Docker's desired config format
type DockerContainer struct {
	DockerID   string `json:"DockerId"`
	DockerName string // needed for linking

	Container *Container
}

// MountPoint describes the in-container location of a Volume and references
// that Volume by name.
type MountPoint struct {
	SourceVolume  string `json:"sourceVolume"`
	ContainerPath string `json:"containerPath"`
	ReadOnly      bool   `json:"readOnly"`
}

// VolumeFrom is a volume which references another container as its source.
type VolumeFrom struct {
	SourceContainer string `json:"sourceContainer"`
	ReadOnly        bool   `json:"readOnly"`
}

// Secret contains all essential attributes needed for ECS secrets vending as environment variables/tmpfs files
type Secret struct {
	Name          string `json:"name"`
	ValueFrom     string `json:"valueFrom"`
	Region        string `json:"region"`
	ContainerPath string `json:"containerPath"`
	Type          string `json:"type"`
	Provider      string `json:"provider"`
	Target        string `json:"target"`
}

// GetSecretResourceCacheKey returns the key required to access the secret
// from the ssmsecret resource
func (s *Secret) GetSecretResourceCacheKey() string {
	return s.ValueFrom + "_" + s.Region
}

// String returns a human readable string representation of DockerContainer
func (dc *DockerContainer) String() string {
	if dc == nil {
		return "nil"
	}
	return fmt.Sprintf("Id: %s, Name: %s, Container: %s", dc.DockerID, dc.DockerName, dc.Container.String())
}

// NewContainerWithSteadyState creates a new Container object with the specified
// steady state. Containers that need the non default steady state set will
// use this method instead of setting it directly
func NewContainerWithSteadyState(steadyState apicontainerstatus.ContainerStatus) *Container {
	steadyStateStatus := steadyState
	return &Container{
		SteadyStateStatusUnsafe: &steadyStateStatus,
	}
}

// KnownTerminal returns true if the container's known status is STOPPED
func (c *Container) KnownTerminal() bool {
	return c.GetKnownStatus().Terminal()
}

// DesiredTerminal returns true if the container's desired status is STOPPED
func (c *Container) DesiredTerminal() bool {
	return c.GetDesiredStatus().Terminal()
}

// GetKnownStatus returns the known status of the container
func (c *Container) GetKnownStatus() apicontainerstatus.ContainerStatus {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.KnownStatusUnsafe
}

// SetKnownStatus sets the known status of the container and update the container
// applied status
func (c *Container) SetKnownStatus(status apicontainerstatus.ContainerStatus) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.KnownStatusUnsafe = status
	c.updateAppliedStatusUnsafe(status)
}

// GetDesiredStatus gets the desired status of the container
func (c *Container) GetDesiredStatus() apicontainerstatus.ContainerStatus {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.DesiredStatusUnsafe
}

// SetDesiredStatus sets the desired status of the container
func (c *Container) SetDesiredStatus(status apicontainerstatus.ContainerStatus) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.DesiredStatusUnsafe = status
}

// GetSentStatus safely returns the SentStatusUnsafe of the container
func (c *Container) GetSentStatus() apicontainerstatus.ContainerStatus {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.SentStatusUnsafe
}

// SetSentStatus safely sets the SentStatusUnsafe of the container
func (c *Container) SetSentStatus(status apicontainerstatus.ContainerStatus) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.SentStatusUnsafe = status
}

// SetKnownExitCode sets exit code field in container struct
func (c *Container) SetKnownExitCode(i *int) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.KnownExitCodeUnsafe = i
}

// GetKnownExitCode returns the container exit code
func (c *Container) GetKnownExitCode() *int {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.KnownExitCodeUnsafe
}

// SetRegistryAuthCredentials sets the credentials for pulling image from ECR
func (c *Container) SetRegistryAuthCredentials(credential credentials.IAMRoleCredentials) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.RegistryAuthentication.ECRAuthData.SetPullCredentials(credential)
}

// ShouldPullWithExecutionRole returns whether this container has its own ECR credentials
func (c *Container) ShouldPullWithExecutionRole() bool {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.RegistryAuthentication != nil &&
		c.RegistryAuthentication.Type == AuthTypeECR &&
		c.RegistryAuthentication.ECRAuthData != nil &&
		c.RegistryAuthentication.ECRAuthData.UseExecutionRole
}

// String returns a human readable string representation of this object
func (c *Container) String() string {
	ret := fmt.Sprintf("%s(%s) (%s->%s)", c.Name, c.Image,
		c.GetKnownStatus().String(), c.GetDesiredStatus().String())
	if c.GetKnownExitCode() != nil {
		ret += " - Exit: " + strconv.Itoa(*c.GetKnownExitCode())
	}
	return ret
}

// GetSteadyStateStatus returns the steady state status for the container. If
// Container.steadyState is not initialized, the default steady state status
// defined by `defaultContainerSteadyStateStatus` is returned. The 'pause'
// container's steady state differs from that of other containers, as the
// 'pause' container can reach its teady state once networking resources
// have been provisioned for it, which is done in the `ContainerResourcesProvisioned`
// state
func (c *Container) GetSteadyStateStatus() apicontainerstatus.ContainerStatus {
	if c.SteadyStateStatusUnsafe == nil {
		return defaultContainerSteadyStateStatus
	}
	return *c.SteadyStateStatusUnsafe
}

// IsKnownSteadyState returns true if the `KnownState` of the container equals
// the `steadyState` defined for the container
func (c *Container) IsKnownSteadyState() bool {
	knownStatus := c.GetKnownStatus()
	return knownStatus == c.GetSteadyStateStatus()
}

// GetNextKnownStateProgression returns the state that the container should
// progress to based on its `KnownState`. The progression is
// incremental until the container reaches its steady state. (The next state of
// `ContainerCreated` and `ContainerRestarting` will both be 'ContainerRunning`.)
// From then on, it transitions to `ContainerStopped`.
//
// For example:
// a. if the steady state of the container is defined as `ContainerRunning`,
// the progression is:
// Container: None -> Pulled -> Created -> Running* -> Stopped -> Zombie
//
// b. if the steady state of the container is defined as `ContainerResourcesProvisioned`,
// the progression is:
// Container: None -> Pulled -> Created -> Running -> Provisioned* -> Stopped -> Zombie
//
// c. if the steady state of the container is defined as `ContainerCreated`,
// the progression is:
// Container: None -> Pulled -> Created* -> Stopped -> Zombie
func (c *Container) GetNextKnownStateProgression() apicontainerstatus.ContainerStatus {
	if c.IsKnownSteadyState() {
		return apicontainerstatus.ContainerStopped
	}

	if c.GetKnownStatus() == apicontainerstatus.ContainerCreated {
		return apicontainerstatus.ContainerRunning
	}

	return c.GetKnownStatus() + 1
}

// IsInternal returns true if the container type is `ContainerCNIPause`
// or `ContainerNamespacePause`. It returns false otherwise
func (c *Container) IsInternal() bool {
	if c.Type == ContainerNormal {
		return false
	}

	return true
}

// IsRunning returns true if the container's known status is either RUNNING
// or RESOURCES_PROVISIONED. It returns false otherwise
func (c *Container) IsRunning() bool {
	return c.GetKnownStatus().IsRunning()
}

// IsMetadataFileUpdated returns true if the metadata file has been once the
// metadata file is ready and will no longer change
func (c *Container) IsMetadataFileUpdated() bool {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.MetadataFileUpdated
}

// SetMetadataFileUpdated sets the container's MetadataFileUpdated status to true
func (c *Container) SetMetadataFileUpdated() {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.MetadataFileUpdated = true
}

// IsEssential returns whether the container is an essential container or not
func (c *Container) IsEssential() bool {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.Essential
}

// AWSLogAuthExecutionRole returns true if the auth is by execution role
func (c *Container) AWSLogAuthExecutionRole() bool {
	return c.LogsAuthStrategy == awslogsAuthExecutionRole
}

// SetCreatedAt sets the timestamp for container's creation time
func (c *Container) SetCreatedAt(createdAt time.Time) {
	if createdAt.IsZero() {
		return
	}
	c.lock.Lock()
	defer c.lock.Unlock()

	c.createdAt = createdAt
}

// SetStartedAt sets the timestamp for container's start time
func (c *Container) SetStartedAt(startedAt time.Time) {
	if startedAt.IsZero() {
		return
	}

	c.lock.Lock()
	defer c.lock.Unlock()

	c.startedAt = startedAt
}

// SetFinishedAt sets the timestamp for container's stopped time
func (c *Container) SetFinishedAt(finishedAt time.Time) {
	if finishedAt.IsZero() {
		return
	}

	c.lock.Lock()
	defer c.lock.Unlock()

	c.finishedAt = finishedAt
}

// GetCreatedAt sets the timestamp for container's creation time
func (c *Container) GetCreatedAt() time.Time {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.createdAt
}

// GetStartedAt sets the timestamp for container's start time
func (c *Container) GetStartedAt() time.Time {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.startedAt
}

// GetFinishedAt sets the timestamp for container's stopped time
func (c *Container) GetFinishedAt() time.Time {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.finishedAt
}

// SetLabels sets the labels for a container
func (c *Container) SetLabels(labels map[string]string) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.labels = labels
}

// SetRuntimeID sets the DockerID for a container
func (c *Container) SetRuntimeID(RuntimeID string) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.RuntimeID = RuntimeID
}

// GetRuntimeID gets the DockerID for a container
func (c *Container) GetRuntimeID() string {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.RuntimeID
}

// GetLabels gets the labels for a container
func (c *Container) GetLabels() map[string]string {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.labels
}

// SetKnownPortBindings sets the ports for a container
func (c *Container) SetKnownPortBindings(ports []PortBinding) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.KnownPortBindingsUnsafe = ports
}

// GetKnownPortBindings gets the ports for a container
func (c *Container) GetKnownPortBindings() []PortBinding {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.KnownPortBindingsUnsafe
}

// SetVolumes sets the volumes mounted in a container
func (c *Container) SetVolumes(volumes []types.MountPoint) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.VolumesUnsafe = volumes
}

// GetVolumes returns the volumes mounted in a container
func (c *Container) GetVolumes() []types.MountPoint {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.VolumesUnsafe
}

// SetNetworkSettings sets the networks field in a container
func (c *Container) SetNetworkSettings(networks *types.NetworkSettings) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.NetworkSettingsUnsafe = networks
}

// GetNetworkSettings returns the networks field in a container
func (c *Container) GetNetworkSettings() *types.NetworkSettings {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.NetworkSettingsUnsafe
}

// SetNetworkMode sets the network mode of the container
func (c *Container) SetNetworkMode(networkMode string) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.NetworkModeUnsafe = networkMode
}

// GetNetworkMode returns the network mode of the container
func (c *Container) GetNetworkMode() string {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.NetworkModeUnsafe
}

// HealthStatusShouldBeReported returns true if the health check is defined in
// the task definition
func (c *Container) HealthStatusShouldBeReported() bool {
	return c.HealthCheckType == DockerHealthCheckType
}

// SetHealthStatus sets the container health status
func (c *Container) SetHealthStatus(health HealthStatus) {
	c.lock.Lock()
	defer c.lock.Unlock()

	if c.Health.Status == health.Status {
		return
	}

	c.Health.Status = health.Status
	c.Health.Since = aws.Time(time.Now())
	c.Health.Output = health.Output

	// Set the health exit code if the health check failed
	if c.Health.Status == apicontainerstatus.ContainerUnhealthy {
		c.Health.ExitCode = health.ExitCode
	}
}

// GetHealthStatus returns the container health information
func (c *Container) GetHealthStatus() HealthStatus {
	c.lock.RLock()
	defer c.lock.RUnlock()

	// Copy the pointer to avoid race condition
	copyHealth := c.Health

	if c.Health.Since != nil {
		copyHealth.Since = aws.Time(aws.TimeValue(c.Health.Since))
	}

	return copyHealth
}

// BuildContainerDependency adds a new dependency container and satisfied status
// to the dependent container
func (c *Container) BuildContainerDependency(contName string,
	satisfiedStatus apicontainerstatus.ContainerStatus,
	dependentStatus apicontainerstatus.ContainerStatus) {
	contDep := ContainerDependency{
		ContainerName:   contName,
		SatisfiedStatus: satisfiedStatus,
	}
	if _, ok := c.TransitionDependenciesMap[dependentStatus]; !ok {
		c.TransitionDependenciesMap[dependentStatus] = TransitionDependencySet{}
	}
	deps := c.TransitionDependenciesMap[dependentStatus]
	deps.ContainerDependencies = append(deps.ContainerDependencies, contDep)
	c.TransitionDependenciesMap[dependentStatus] = deps
}

// BuildResourceDependency adds a new resource dependency by taking in the required status
// of the resource that satisfies the dependency and the dependent container status,
// whose transition is dependent on the resource.
// example: if container's PULLED transition is dependent on volume resource's
// CREATED status, then RequiredStatus=VolumeCreated and dependentStatus=ContainerPulled
func (c *Container) BuildResourceDependency(resourceName string,
	requiredStatus resourcestatus.ResourceStatus,
	dependentStatus apicontainerstatus.ContainerStatus) {

	resourceDep := ResourceDependency{
		Name:           resourceName,
		RequiredStatus: requiredStatus,
	}
	if _, ok := c.TransitionDependenciesMap[dependentStatus]; !ok {
		c.TransitionDependenciesMap[dependentStatus] = TransitionDependencySet{}
	}
	deps := c.TransitionDependenciesMap[dependentStatus]
	deps.ResourceDependencies = append(deps.ResourceDependencies, resourceDep)
	c.TransitionDependenciesMap[dependentStatus] = deps
}

// updateAppliedStatusUnsafe updates the container transitioning status
func (c *Container) updateAppliedStatusUnsafe(knownStatus apicontainerstatus.ContainerStatus) {
	if c.AppliedStatus == apicontainerstatus.ContainerStatusNone {
		return
	}

	// Check if the container transition has already finished
	if c.AppliedStatus <= knownStatus {
		c.AppliedStatus = apicontainerstatus.ContainerStatusNone
	}
}

// SetAppliedStatus sets the applied status of container and returns whether
// the container is already in a transition
func (c *Container) SetAppliedStatus(status apicontainerstatus.ContainerStatus) bool {
	c.lock.Lock()
	defer c.lock.Unlock()

	if c.AppliedStatus != apicontainerstatus.ContainerStatusNone {
		// return false to indicate the set operation failed
		return false
	}

	c.AppliedStatus = status
	return true
}

// GetAppliedStatus returns the transitioning status of container
func (c *Container) GetAppliedStatus() apicontainerstatus.ContainerStatus {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.AppliedStatus
}

// ShouldPullWithASMAuth returns true if this container needs to retrieve
// private registry authentication data from ASM
func (c *Container) ShouldPullWithASMAuth() bool {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.RegistryAuthentication != nil &&
		c.RegistryAuthentication.Type == AuthTypeASM &&
		c.RegistryAuthentication.ASMAuthData != nil
}

// SetASMDockerAuthConfig add the docker auth config data to the
// RegistryAuthentication struct held by the container, this is then passed down
// to the docker client to pull the image
func (c *Container) SetASMDockerAuthConfig(dac types.AuthConfig) {
	c.RegistryAuthentication.ASMAuthData.SetDockerAuthConfig(dac)
}

// SetV3EndpointID sets the v3 endpoint id of container
func (c *Container) SetV3EndpointID(v3EndpointID string) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.V3EndpointID = v3EndpointID
}

// GetV3EndpointID returns the v3 endpoint id of container
func (c *Container) GetV3EndpointID() string {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.V3EndpointID
}

// InjectV3MetadataEndpoint injects the v3 metadata endpoint as an environment variable for a container
func (c *Container) InjectV3MetadataEndpoint() {
	c.lock.Lock()
	defer c.lock.Unlock()

	// don't assume that the environment variable map has been initialized by others
	if c.Environment == nil {
		c.Environment = make(map[string]string)
	}

	c.Environment[MetadataURIEnvironmentVariableName] =
		fmt.Sprintf(MetadataURIFormat, c.V3EndpointID)
}

// ShouldCreateWithSSMSecret returns true if this container needs to get secret
// value from SSM Parameter Store
func (c *Container) ShouldCreateWithSSMSecret() bool {
	c.lock.RLock()
	defer c.lock.RUnlock()

	// Secrets field will be nil if there is no secrets for container
	if c.Secrets == nil {
		return false
	}

	for _, secret := range c.Secrets {
		if secret.Provider == SecretProviderSSM {
			return true
		}
	}
	return false
}

// ShouldCreateWithASMSecret returns true if this container needs to get secret
// value from AWS Secrets Manager
func (c *Container) ShouldCreateWithASMSecret() bool {
	c.lock.RLock()
	defer c.lock.RUnlock()

	// Secrets field will be nil if there is no secrets for container
	if c.Secrets == nil {
		return false
	}

	for _, secret := range c.Secrets {
		if secret.Provider == SecretProviderASM {
			return true
		}
	}
	return false
}

// MergeEnvironmentVariables appends additional envVarName:envVarValue pairs to
// the the container's enviornment values structure
func (c *Container) MergeEnvironmentVariables(envVars map[string]string) {
	c.lock.Lock()
	defer c.lock.Unlock()

	// don't assume that the environment variable map has been initialized by others
	if c.Environment == nil {
		c.Environment = make(map[string]string)
	}
	for k, v := range envVars {
		c.Environment[k] = v
	}
}

func (c *Container) HasSecretAsEnvOrLogDriver() bool {
	c.lock.RLock()
	defer c.lock.RUnlock()

	// Secrets field will be nil if there is no secrets for container
	if c.Secrets == nil {
		return false
	}
	for _, secret := range c.Secrets {
		if secret.Type == SecretTypeEnv || secret.Target == SecretTargetLogDriver {
			return true
		}
	}
	return false
}

func (c *Container) GetStartTimeout() time.Duration {
	c.lock.Lock()
	defer c.lock.Unlock()

	return time.Duration(c.StartTimeout) * time.Second
}

func (c *Container) GetStopTimeout() time.Duration {
	c.lock.Lock()
	defer c.lock.Unlock()

	return time.Duration(c.StopTimeout) * time.Second
}

func (c *Container) SetRestartPolicy(policy RestartPolicy) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.RestartInfo != nil {
		c.RestartInfo.RestartPolicy = policy
	}
}

func (c *Container) GetRestartPolicy() RestartPolicy {
	c.lock.RLock()
	defer c.lock.RUnlock()
	if c.RestartInfo == nil {
		return 0
	}
	return c.RestartInfo.RestartPolicy
}

func (c *Container) SetRestartAttempts(count RestartCount) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.RestartInfo != nil {
		c.RestartInfo.RestartAttempts = count
	}
}

func (c *Container) GetRestartAttempts() RestartCount {
	c.lock.RLock()
	defer c.lock.RUnlock()
	if c.RestartInfo == nil {
		return 0
	}
	return c.RestartInfo.RestartAttempts
}

func (c *Container) SetRestartBackoff(backoff *retry.ExponentialBackoff) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.RestartInfo != nil {
		c.RestartInfo.RestartBackoff = backoff
	}
}

func (c *Container) GetRestartBackoff() *retry.ExponentialBackoff {
	c.lock.RLock()
	defer c.lock.RUnlock()
	if c.RestartInfo == nil {
		return nil
	}
	return c.RestartInfo.RestartBackoff
}

func (c *Container) IncrementRestartAttempts() {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.RestartInfo != nil {
		c.RestartInfo.RestartAttempts += 1
	}
}

func (c *Container) CanMakeRestartAttempt() bool {
	c.lock.RLock()
	defer c.lock.RUnlock()
	if c.RestartInfo == nil {
		return false
	}
	return c.RestartInfo.RestartPolicy == UnlessTaskStopped ||
		c.RestartInfo.RestartPolicy == OnFailure &&
		c.RestartInfo.RestartAttempts < c.RestartInfo.RestartMaxAttempts
}

func (c *Container) IsAutoRestartNonEssentialContainer() bool {
	return !c.IsEssential() && c.RestartInfo != nil && c.RestartInfo.RestartPolicy != Never
}

func (c *Container) IsDesiredToRestartWhenReceivingStopped() bool {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.RestartInfo == nil {
		return false
	}
	defer func() {
		c.RestartInfo.DesiredToRestartWhenReceivingStopped = false
	}()
	return c.RestartInfo.DesiredToRestartWhenReceivingStopped
}

func (c *Container) IsDesiredToFullyStopWhenReceivingStopped() bool {
	c.lock.RLock()
	defer c.lock.RUnlock()
	if c.RestartInfo == nil {
		return false
	}
	return c.RestartInfo.DesiredToFullyStopWhenReceivingStopped
}

func (c *Container) SetDesiredToFullyStopWhenReceivingStopped() {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.RestartInfo != nil {
		c.RestartInfo.DesiredToFullyStopWhenReceivingStopped = true
	}
}

func (c *Container) SetDesiredToRestartWhenReceivingStopped() {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.RestartInfo != nil {
		c.RestartInfo.DesiredToRestartWhenReceivingStopped = true
	}
}

func (c *Container) SetDefaultRestartMaxAttemptsOnFailure() {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.RestartInfo != nil {
		if c.RestartInfo.RestartPolicy == OnFailure && c.RestartInfo.RestartMaxAttempts == 0 {
			c.RestartInfo.RestartMaxAttempts = DefaultRestartMaxAttemptsOnFailure
		}
	}
}

