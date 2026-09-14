# LAB-16894 Print Gateway — Nomad job spec.
#
# See ../DEPLOYMENT.md ("Path C — Production: Nomad + Consul + Traefik") for
# the full deploy runbook this file is part of. Every value that must change
# per cluster/deployment is marked "CHANGE ME" below — search for that string
# before running `nomad job run`.
#
# Assumptions this spec makes (see DEPLOYMENT.md §C1 if any don't hold for
# your cluster):
#   - CUPS is reachable on the same host every eligible Nomad client runs on
#     (network_mode = "host", CUPS_HOST left at its "localhost" default in
#     docker-entrypoint.sh). If CUPS instead lives on a separate fixed host,
#     switch network.mode to "bridge", add a "port" block under a NAT'd
#     dynamic port, and set CUPS_HOST/CUPS_PORT in the env stanza below.
#   - This cluster's Vault is what supplies PRINT_GATEWAY_TOKEN (via the
#     internal/secrets Vault path, README.md's "Secrets (Vault)" section) —
#     the vault/template stanzas below are the "no plain env var" path.
#     If this deployment instead uses a plain shared secret (e.g. a Nomad
#     Variable), delete the vault{} and template{} blocks and set
#     PRINT_GATEWAY_TOKEN directly in the env{} block instead — both are
#     shown, commented, at the bottom of the task stanza.
#   - Traefik discovers services via its Consul Catalog provider, so the
#     tags below (traefik.enable=true, a router rule) are all Traefik needs;
#     no separate Traefik static/dynamic config file is required for this
#     service specifically.

job "printgateway" {
  datacenters = ["dc1"]          # CHANGE ME
  # namespace = "default"        # CHANGE ME if this cluster uses namespaces
  type        = "service"

  update {
    max_parallel      = 1
    canary            = 1
    min_healthy_time  = "30s"
    healthy_deadline  = "5m"
    progress_deadline = "10m"
    auto_revert       = true
  }

  group "printgateway" {
    count = 1  # bump for more replicas; the process holds no local state,
               # so horizontal scale-out is safe as-is (no queue/DB to share)

    network {
      # "host": simplest correct choice when CUPS runs on the same host as
      # this allocation (see the header comment above). Nomad still
      # allocates a dynamic port under host networking — it just doesn't
      # remap it, so the label below still resolves via NOMAD_PORT_http.
      mode = "host"
      port "http" {}
    }

    service {
      name = "printgateway"
      port = "http"
      provider = "consul"

      tags = [
        "traefik.enable=true",
        "traefik.http.routers.printgateway.rule=Host(`printgateway.CHANGE-ME.example.com`)",
        "traefik.http.routers.printgateway.entrypoints=websecure",
        "traefik.http.routers.printgateway.tls=true",
      ]

      # Consul's own health check — this is the "production registration"
      # README.md's "Health check" section says belongs to the Nomad job
      # spec, not to printgateway's own -consul-register flag (that flag is
      # local-dev only and is never passed in this job).
      check {
        type     = "http"
        path     = "/status"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "printgateway" {
      driver = "docker"

      config {
        image        = "your-registry.example.com/printgateway:CHANGE-ME"  # CHANGE ME
        network_mode = "host"
        # No "ports" map needed under host networking — the container binds
        # directly into the allocation's (== host's) network namespace.
      }

      # Option A (default here): resolve the print token from Vault via
      # this cluster's Nomad-Vault integration, rendered into a file the
      # task reads at startup. Requires this job to run under a Nomad
      # client/policy that can reach Vault.
      vault {
        policies = ["printgateway"]  # CHANGE ME to this cluster's actual policy name
      }

      template {
        # internal/secrets already knows how to read this path/key when
        # VAULT_ADDR/SECRET_STORE_URL is set (see README.md's "Secrets
        # (Vault)" section) — this template only needs to hand it the
        # connection details, not the token itself; the token resolution
        # happens inside printgateway at startup, straight from Vault.
        data = <<EOH
VAULT_ADDR={{ env "VAULT_ADDR" }}
VAULT_TOKEN={{ env "VAULT_TOKEN" }}
EOH
        destination = "secrets/vault.env"
        env         = true
      }

      # --- Optional: JSON config file (see DEPLOYMENT.md "C1.5 — Do you
      # need printservice.config.json?" before enabling this) -------------
      # Leave commented out for the common case: every setting below comes
      # from env{}/vault{} alone, which is simpler and needs nothing here.
      # Uncomment only if this deployment wants a setting (most commonly the
      # S3 access/secret key pair) supplied as a file rendered from Vault at
      # start, per README.md's "Configuration file" section. This is the
      # Nomad-native version of that section's "a file rendered from Vault
      # at process start, e.g. a Vault Agent template" deployment model —
      # Nomad's own template{} + vault{} integration does the rendering, no
      # separate Vault Agent process needed.
      #
      # template {
      #   data = <<EOH
      # {
      #   "resource/file_storage": {
      #     "host": "s3.CHANGE-ME.example.com",
      #     "s3-user": "{{ with secret "<LABOS_ENV>/config/print_gateway" }}{{ .Data.data.s3-access-key }}{{ end }}",
      #     "s3-password": "{{ with secret "<LABOS_ENV>/config/print_gateway" }}{{ .Data.data.s3-secret-key }}{{ end }}"
      #   },
      #   "resource/printgateway": {
      #     "objectStore": { "bucket": "CHANGE-ME", "region": "CHANGE-ME" }
      #   }
      # }
      # EOH
      #   destination = "secrets/printservice.config.json"
      #   perms       = "600"
      # }
      #
      # Then add to the env{} block below:
      #   PRINT_GATEWAY_CONFIG = "${NOMAD_SECRETS_DIR}/printservice.config.json"
      # ----------------------------------------------------------------------

      env {
        PORT                       = "${NOMAD_PORT_http}"
        PRINT_GATEWAY_BIND_HOST    = "0.0.0.0"
        PRINT_GATEWAY_REQUIRE_AUTH = "true"

        # LABOS_ENV = "production"          # CHANGE ME — path prefix under the Vault KV mount
        # LOG_SERVER = "logstash.internal:514"  # CHANGE ME, or omit for console-only logging

        # --- Option B: plain shared-secret deployment (no Vault) ---------
        # Delete the vault{}/template{} blocks above and this whole option
        # A's env vars, then instead set the token from a Nomad Variable:
        #
        # PRINT_GATEWAY_TOKEN = "${NOMAD_VAR_print_gateway_token}"
        # -------------------------------------------------------------------
      }

      resources {
        cpu    = 200   # MHz — CHANGE ME based on observed load
        memory = 128   # MB  — CHANGE ME based on observed load
      }
    }
  }
}
