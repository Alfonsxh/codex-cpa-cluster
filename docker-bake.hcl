variable "PLATFORM" {
  default = "linux/amd64"
}

variable "CONTROL_DIGEST" {
  default = ""
}

variable "WEB_DIGEST" {
  default = ""
}

variable "GATEWAY_DIGEST" {
  default = ""
}

variable "EDGE_DIGEST" {
  default = ""
}

group "default" {
  targets = ["control", "web", "gateway", "edge"]
}

target "release-component" {
  context    = "."
  dockerfile = "Dockerfile"
  platforms  = [PLATFORM]
}

target "control" {
  inherits = ["release-component"]
  target   = "control"
  tags     = ["codex-cpa-control:sha256-${CONTROL_DIGEST}"]
  labels = {
    "io.codex-cpa.component"        = "control"
    "io.codex-cpa.component-digest" = CONTROL_DIGEST
    "io.codex-cpa.source-digest"    = CONTROL_DIGEST
  }
}

target "web" {
  inherits = ["release-component"]
  target   = "web"
  tags     = ["codex-cpa-web:sha256-${WEB_DIGEST}"]
  labels = {
    "io.codex-cpa.component"        = "web"
    "io.codex-cpa.component-digest" = WEB_DIGEST
    "io.codex-cpa.source-digest"    = WEB_DIGEST
  }
}

target "gateway" {
  inherits = ["release-component"]
  target   = "gateway"
  tags     = ["codex-cpa-gateway:sha256-${GATEWAY_DIGEST}"]
  labels = {
    "io.codex-cpa.component"        = "gateway"
    "io.codex-cpa.component-digest" = GATEWAY_DIGEST
    "io.codex-cpa.source-digest"    = GATEWAY_DIGEST
  }
}

target "edge" {
  inherits = ["release-component"]
  target   = "edge"
  tags     = ["codex-cpa-edge:sha256-${EDGE_DIGEST}"]
  labels = {
    "io.codex-cpa.component"        = "edge"
    "io.codex-cpa.component-digest" = EDGE_DIGEST
    "io.codex-cpa.source-digest"    = EDGE_DIGEST
  }
}
