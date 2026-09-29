# Official Python manifest verified on 2026-09-28; runtime has no package acquisition.
FROM python:3.12-slim-bookworm@sha256:392307d22300de8b5986851a12d9176dfc0fc073e65bf6523ebd7dcbeb23564e AS dependencies
WORKDIR /opt/source-parser
COPY requirements.lock ./
RUN python -m venv /opt/venv && /opt/venv/bin/pip install --no-cache-dir --require-hashes --only-binary=:all: -r requirements.lock
FROM dependencies AS build
COPY runtime.py mybatis_parser.py prefetch.py ./
RUN /opt/venv/bin/python prefetch.py --cache /opt/source-parser/grammar

FROM python:3.12-slim-bookworm@sha256:392307d22300de8b5986851a12d9176dfc0fc073e65bf6523ebd7dcbeb23564e
WORKDIR /opt/source-parser
ENV PATH=/opt/venv/bin:$PATH SOURCE_PARSER_CACHE=/opt/source-parser/grammar PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
COPY --from=build /opt/venv /opt/venv
COPY --from=build /opt/source-parser/grammar ./grammar
COPY runtime.py mybatis_parser.py server.py ./
COPY LICENSE-*.txt ./
RUN chown -R 65532:65532 /opt/source-parser/grammar && chmod -R a+rX,a-w /opt/source-parser /opt/venv
USER 65532:65532
EXPOSE 8081
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8081/health',timeout=3)"
CMD ["python", "server.py"]
