# Running K8s in Production

Teams running K8s in production should pin their container images,
configure resource requests and limits per pod, and use network
policies by default. K8s probes (liveness and readiness) determine
traffic routing. See the architecture overview for control-plane
details.
