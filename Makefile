.PHONY: terminal docs docs-install docs-build

DOCS_MODULES := documentation/node_modules/.package-lock.json

terminal:
	go run ./playground/terminal

docs: $(DOCS_MODULES)
	npm --prefix documentation run start

docs-install:
	npm --prefix documentation install

docs-build: $(DOCS_MODULES)
	npm --prefix documentation run build

$(DOCS_MODULES): documentation/package.json documentation/package-lock.json
	npm --prefix documentation install
