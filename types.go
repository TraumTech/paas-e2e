package e2e

import (
	"encoding/json"
	"time"
)

type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Product struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Stage string `json:"stage"`
}

type ProductList struct {
	Products []Product `json:"products"`
}

type Service struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	RepoURL   string    `json:"repo_url"`
	Product   Product   `json:"product"`
	CreatedAt time.Time `json:"created_at"`
}

type Version struct {
	ID             string    `json:"id"`
	Number         int64     `json:"number"`
	Image          string    `json:"image"`
	Branch         string    `json:"branch"`
	CommitRevision string    `json:"commit_revision"`
	Environment    string    `json:"environment"`
	CreatedAt      time.Time `json:"created_at"`
}

type ProtocolRef struct {
	Name          string    `json:"name"`
	Format        string    `json:"format"`
	ServiceID     string    `json:"service_id"`
	VersionNumber int64     `json:"version_number"`
	PublishedAt   time.Time `json:"published_at"`
}

type ProtocolList struct {
	ServiceID string        `json:"service_id"`
	Protocols []ProtocolRef `json:"protocols"`
}

type Protocol struct {
	Name         string          `json:"name"`
	Format       string          `json:"format"`
	Published    bool            `json:"published"`
	DocumentText string          `json:"document_text"`
	Document     json.RawMessage `json:"document"`
	PublishedAt  time.Time       `json:"published_at"`
}

type Cluster struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Kind          string    `json:"kind"`
	Environment   string    `json:"environment"`
	Endpoint      string    `json:"endpoint"`
	Connected     bool      `json:"connected"`
	LastContactAt time.Time `json:"last_contact_at"`
}

type ClusterList struct {
	Clusters []Cluster `json:"clusters"`
}

type Pod struct {
	Name     string `json:"name"`
	Phase    string `json:"phase"`
	Ready    bool   `json:"ready"`
	Restarts int64  `json:"restarts"`
	Reason   string `json:"reason"`
	Message  string `json:"message"`
}

type Process struct {
	Name            string `json:"name"`
	Image           string `json:"image"`
	DesiredReplicas int64  `json:"desired_replicas"`
	ReadyReplicas   int64  `json:"ready_replicas"`
	Pods            []Pod  `json:"pods"`
}

type WorkloadStatus struct {
	Deployed  bool      `json:"deployed"`
	Processes []Process `json:"processes"`
}

type ServiceRoute struct {
	ZoneSlug    string `json:"zone_slug"`
	Prefix      string `json:"prefix"`
	ServiceName string `json:"service_name"`
}

type DeclaredRoute struct {
	ZoneSlug    string `json:"zone_slug"`
	Host        string `json:"host"`
	Prefix      string `json:"prefix"`
	ServiceName string `json:"service_name"`
	ProcessName string `json:"process_name"`
	Environment string `json:"environment"`
}

type RouteList struct {
	Routes   []ServiceRoute  `json:"routes"`
	Declared []DeclaredRoute `json:"declared"`
}

type ConsumerCompatibility struct {
	ConsumerServiceName   string `json:"consumer_service_name"`
	ConsumerVersionNumber int64  `json:"consumer_version_number"`
	Breaking              bool   `json:"breaking"`
	Comparable            bool   `json:"comparable"`
}

type CompatibilityReport struct {
	Breaking  bool                    `json:"breaking"`
	Consumers []ConsumerCompatibility `json:"consumers"`
}

type Build struct {
	ID             string    `json:"id"`
	Branch         string    `json:"branch"`
	CommitRevision string    `json:"commit_revision"`
	Image          string    `json:"image"`
	HasContract    bool      `json:"has_contract"`
	CreatedAt      time.Time `json:"created_at"`
}

type BuildList struct {
	Builds []Build `json:"builds"`
}

type Deployment struct {
	Environment string `json:"environment"`
	ClusterName string `json:"cluster_name"`
	Namespace   string `json:"namespace"`
}

type ScmConnection struct {
	ID           string `json:"id"`
	Provider     string `json:"provider"`
	AccountLogin string `json:"account_login"`
	AccountType  string `json:"account_type"`
}

type ScmConnectionList struct {
	Connections []ScmConnection `json:"connections"`
}

type InstallIntent struct {
	InstallURL string `json:"install_url"`
}

type RegistryCredential struct {
	Server   string `json:"server"`
	Username string `json:"username"`
}

type RegistryCredentialList struct {
	Credentials []RegistryCredential `json:"credentials"`
}

type ZoneHost struct {
	Environment string `json:"environment"`
	BaseHost    string `json:"base_host"`
}

type DomainZone struct {
	Slug  string     `json:"slug"`
	Name  string     `json:"name"`
	Hosts []ZoneHost `json:"hosts"`
}

type DomainZoneList struct {
	Zones []DomainZone `json:"zones"`
}
