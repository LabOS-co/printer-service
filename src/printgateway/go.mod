module printgateway

go 1.25.0

require (
	github.com/LabOS-co/go-packages/error_handler v1.2.4
	github.com/LabOS-co/go-packages/logs v1.5.2
)

require (
	github.com/cenkalti/backoff/v3 v3.0.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.5.0 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/go-retryablehttp v0.6.6 // indirect
	github.com/hashicorp/go-rootcerts v1.0.2 // indirect
	github.com/hashicorp/go-secure-stdlib/parseutil v0.1.6 // indirect
	github.com/hashicorp/go-secure-stdlib/strutil v0.1.2 // indirect
	github.com/hashicorp/go-sockaddr v1.0.2 // indirect
	github.com/hashicorp/hcl v1.0.0 // indirect
	github.com/hashicorp/vault/api v1.9.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/compress v1.17.4 // indirect
	github.com/klauspost/cpuid/v2 v2.2.6 // indirect
	github.com/minio/md5-simd v1.1.2 // indirect
	github.com/minio/minio-go/v7 v7.0.66 // indirect
	github.com/minio/sha256-simd v1.0.1 // indirect
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/rs/xid v1.5.0 // indirect
	github.com/ryanuber/go-glob v1.0.0 // indirect
	github.com/samber/lo v1.39.0 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/exp v0.0.0-20220303212507-bbda1eaf7a17 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/time v0.0.0-20200416051211-89c76fbcd5d1 // indirect
	gopkg.in/ini.v1 v1.67.0 // indirect
	gopkg.in/square/go-jose.v2 v2.5.1 // indirect
)

require (
	github.com/LabOS-co/go-packages/cloud_storage v1.0.4
	github.com/LabOS-co/go-packages/encryption v1.1.1
	github.com/LabOS-co/go-packages/secret_store v1.2.4
	github.com/LabOS-co/go-packages/shared v1.2.1 // indirect
	github.com/TwiN/go-color v1.4.1 // indirect
	github.com/bshuster-repo/logrus-logstash-hook v1.1.0 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/shirou/gopsutil v3.21.11+incompatible // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/yusufpapurcu/wmi v1.2.3 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.38.0 // indirect
)

// cloud_storage's presign/streaming methods (CloudStorageStreamingClient)
// aren't tagged yet — see go-packages' feature/cloud_storage/LAB-16894—
// Add_presign_and_streaming_support branch. Same pattern as secret_store
// below: remove this and bump the require above to a real tag once that
// branch is merged/tagged.
replace github.com/LabOS-co/go-packages/cloud_storage => ../../../go-packages/cloud_storage

// Points at a separate git worktree (not ../../../go-packages/secret_store)
// because the main go-packages checkout now sits on the cloud_storage
// feature branch for the replace above, and a single working directory
// can't be on two branches at once.
replace github.com/LabOS-co/go-packages/secret_store => ../../../go-packages-secret_store-wt/secret_store
