# Set up the local directory to run the demo pages.
default:
	cd ../.. && bower install
	ln -sf ../../third_party/bower_components .
	rm -rf res/
	mkdir -p res/imp
	ln -sfT ../../../../third_party/bower_components res/imp/bower_components
	ln -sfT ../../../../res  res/common
	ln -sfT ../../../res/js res/js
	ln -sfT ../../../res/img res/img
	npm install http-server

	# # We use sinon to mock up the demo page's json requests.
	npm install sinon@1.17.2
	cp ../../node_modules/sinon/pkg/sinon-1.17.2.js res/imp/sinon-1.17.2.js

# Run a local HTTP server for the demo pages.
run:
	# We download these here because the default target is called by unittests
	wget --no-clobber https://storage.googleapis.com/skia-infra-testdata/status-demo/commits-table-35-demo.json
	wget --no-clobber https://storage.googleapis.com/skia-infra-testdata/status-demo/commits-canvas-demo.json
	wget --no-clobber https://storage.googleapis.com/skia-infra-testdata/status-demo/autoroll-widget-demo.json
	wget --no-clobber https://storage.googleapis.com/skia-infra-testdata/status-demo/alert-widget-demo.json
	wget --no-clobber https://storage.googleapis.com/skia-infra-testdata/status-demo/status-infra-demo.json
	../../node_modules/.bin/http-server -p 8080 -a 127.0.0.1
