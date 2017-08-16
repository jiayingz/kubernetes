TAG?=v0.3
REGISTRY?=gcr.io/jiayingz-gke-dev
IMAGE=device-plugin-gpu
build:
	go build nvidia_gpu.go
	docker build --pull -t ${REGISTRY}/${IMAGE}:${TAG} .
push:
	gcloud docker -- push ${REGISTRY}/${IMAGE}:${TAG}
.PHONY: build push
