# Official Python manifest verified on 2026-09-28; runtime has no package acquisition.
FROM python:3.12-slim-bookworm@sha256:392307d22300de8b5986851a12d9176dfc0fc073e65bf6523ebd7dcbeb23564e AS dependencies
WORKDIR /opt/source-parser
COPY requirements.lock ./
RUN python -m venv /opt/venv && /opt/venv/bin/pip install --no-cache-dir --require-hashes --only-binary=:all: -r requirements.lock
FROM dependencies AS build
COPY runtime.py mybatis_parser.py prefetch.py ./
RUN /opt/venv/bin/python prefetch.py --cache /opt/source-parser/grammar

FROM dependencies AS sfc-build
ARG TARGETARCH
COPY install_node.py ./
COPY sfc/package.json sfc/package-lock.json sfc/runtime.lock.json ./sfc/
RUN case "$TARGETARCH" in amd64) node_arch=x64 ;; arm64) node_arch=arm64 ;; *) exit 1 ;; esac \
    && python install_node.py --arch "$node_arch" --output /opt/node \
    && /opt/node/bin/node /opt/node/lib/node_modules/npm/bin/npm-cli.js --prefix /opt/source-parser/sfc ci --ignore-scripts --no-audit --no-fund
COPY sfc/parse_sfc.cjs ./sfc/

FROM python:3.12-slim-bookworm@sha256:392307d22300de8b5986851a12d9176dfc0fc073e65bf6523ebd7dcbeb23564e
WORKDIR /opt/source-parser
ENV PATH=/opt/venv/bin:$PATH SOURCE_PARSER_CACHE=/opt/source-parser/grammar SOURCE_PARSER_NODE=/usr/local/bin/node PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
COPY --from=build /opt/venv /opt/venv
COPY --from=build /opt/source-parser/grammar ./grammar
COPY --from=sfc-build /opt/node/bin/node /usr/local/bin/node
COPY --from=sfc-build /opt/node/LICENSE ./sfc/LICENSE-node.txt
COPY --from=sfc-build /opt/source-parser/sfc ./sfc
COPY runtime.py mybatis_parser.py server.py ./
COPY LICENSE-*.txt ./
RUN chown -R 65532:65532 /opt/source-parser/grammar /opt/source-parser/sfc && chmod -R a+rX,a-w /opt/source-parser /opt/venv /usr/local/bin/node
USER 65532:65532
EXPOSE 8081
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8081/health',timeout=3)"
CMD ["python", "server.py"]
