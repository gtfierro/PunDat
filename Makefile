APP?=pundat
RELEASE?=0.3.2
COMMIT?=$(shell git rev-parse --short HEAD)
PROJECT?=github.com/gtfierro/pundat
PERSISTDIR?=/etc/pundat

clean:
	rm -f ${APP}

build: clean
	go build \
		-ldflags "-s -w -X ${PROJECT}/version.Release=${RELEASE} \
						-X ${PROJECT}/version.Commit=${COMMIT}" \
						-o ${APP}
run: build
		./${APP}

container: build
	cp pundat container
	docker build -t gtfierro/$(APP):$(RELEASE) container
	docker build -t gtfierro/$(APP):latest container

push: container
	docker push gtfierro/$(APP):$(RELEASE)
	docker push gtfierro/$(APP):latest

containerRun: container
	docker stop $(APP):$(RELEASE) || true && docker rm $(APP):$(RELEASE) || true
	docker run --name $(APP) \
			   --mount type=bind,source=$(shell pwd)/$(PERSISTDIR),target=/etc/hod \
			   -it \
			   -e BW2_AGENT=$(BW2_AGENT) -e BW2_DEFAULT_ENTITY=$(BW2_DEFAULT_ENTITY) \
			   --rm \
			   gtfierro/$(APP):$(RELEASE)
