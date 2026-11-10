.PHONY: test ui e2e test-external

test:
	go test -coverprofile=coverage.out ./...

ui:
	cd ui && npm test

e2e:
	bash e2e/run.sh $(SHARD)

test-external:
	bash external/smoke.sh
