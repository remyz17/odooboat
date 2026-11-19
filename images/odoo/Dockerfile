# syntax=docker/dockerfile:1
# check=skip=InvalidDefaultArgInFrom
#
# Odooboat base image, contract 1 (ADR 0008 §4, amendment A2).
#
# One image per Odoo series and Python version, for linux/amd64 and
# linux/arm64. It contains no Odoo source: the environment mounts or copies it
# at /opt/odoo/community and /opt/odoo/enterprise.
#
# Build arguments come from images/matrix.yaml through `go run ./images/ci
# matrix`; do not hard-code series values here. The series requirements are
# passed as the named build context "series" holding requirements.txt:
#
#   docker buildx build images/odoo \
#     --build-context series=<dir with requirements.txt> \
#     --build-arg SERIES=18.0 --build-arg PYTHON=3.12 \
#     --build-arg DISTRO_IMAGE=debian:bookworm-slim \
#     --build-arg WKHTMLTOPDF_VERSION=0.12.6.1-3 --build-arg WKHTMLTOPDF_BUILD=bookworm \
#     --build-arg ODOO_REVISION=<commit of requirements.txt>

ARG DISTRO_IMAGE
ARG UV_IMAGE=ghcr.io/astral-sh/uv:0.13.9@sha256:d3b601f0c7b118bc1753febf7c41cc80981df678445e0254284eecf84db79a7d

FROM ${UV_IMAGE} AS uv

# The builder compiles the series requirements. Compilers and headers never
# reach the published image.
FROM ${DISTRO_IMAGE} AS builder
ARG PYTHON
ENV UV_PYTHON_INSTALL_DIR=/opt/odooboat/python \
    UV_PYTHON_PREFERENCE=only-managed \
    UV_NO_CACHE=1 \
    UV_LINK_MODE=copy
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        build-essential pkg-config ca-certificates \
        libpq-dev libldap2-dev libsasl2-dev libxml2-dev libxslt1-dev \
        libjpeg-dev zlib1g-dev libfreetype-dev liblcms2-dev libwebp-dev \
        libffi-dev libssl-dev; \
    rm -rf /var/lib/apt/lists/*
COPY --from=uv /uv /usr/local/bin/uv
RUN set -eux; \
    test -n "$PYTHON"; \
    uv python install "$PYTHON"; \
    uv venv --python "$PYTHON" /opt/odooboat/venv
# The series requirements warm the environment image build (ADR 0008 §4).
# ODOO_REVISION records the Odoo commit they were read from.
ARG ODOO_REVISION=unknown
COPY --from=series requirements.txt /opt/odooboat/base/requirements.txt
RUN printf '%s\n' "$ODOO_REVISION" > /opt/odooboat/base/odoo-revision
# pip builds a wheelhouse for the selected Python, then uv installs from it
# without an index. uv rejects some legacy sdists pinned by older series (for
# example PyPDF2 1.26.0, whose tar archive uv's strict reader refuses), while
# pip extracts them with Python's tarfile.
#
# reportlab 4 moved its bitmap renderer, used by Odoo for barcodes, to
# rl_renderPM. Odoo's Debian packaging installs it (python3-renderpm) while
# requirements.txt lists it for Windows only.
ARG PIP_VERSION=26.3
ARG RL_RENDERPM_VERSION=4.0.3
COPY build-constraints.txt /tmp/build-constraints.txt
RUN set -eux; \
    venv=/opt/odooboat/venv/bin/python; \
    pip() { uv tool run --python "$PYTHON" --from "pip==$PIP_VERSION" pip "$@"; }; \
    pip wheel --disable-pip-version-check --build-constraint /tmp/build-constraints.txt --wheel-dir /tmp/wheels \
        --requirement /opt/odooboat/base/requirements.txt; \
    uv pip install --python "$venv" --no-index --find-links /tmp/wheels \
        --requirement /opt/odooboat/base/requirements.txt; \
    if "$venv" -c 'import reportlab, sys; sys.exit(int(reportlab.Version.split(".")[0]) < 4)'; then \
        pip wheel --disable-pip-version-check --build-constraint /tmp/build-constraints.txt --no-deps --wheel-dir /tmp/wheels "rl-renderPM==$RL_RENDERPM_VERSION"; \
        uv pip install --python "$venv" --no-index --find-links /tmp/wheels "rl-renderPM==$RL_RENDERPM_VERSION"; \
    fi; \
    rm -rf /tmp/wheels /tmp/build-constraints.txt

FROM ${DISTRO_IMAGE}
ARG SERIES
ARG PYTHON
ARG WKHTMLTOPDF_VERSION
ARG WKHTMLTOPDF_BUILD
ARG TARGETARCH

LABEL odooboat.image.contract="1" \
      odooboat.image.series="${SERIES}" \
      odooboat.image.python="${PYTHON}" \
      odooboat.image.kind="base"

ENV LANG=C.UTF-8 \
    HOME=/var/lib/odoo \
    VIRTUAL_ENV=/opt/odooboat/venv \
    PATH=/opt/odooboat/venv/bin:/opt/odooboat/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    PYTHONUNBUFFERED=1 \
    UV_PYTHON_INSTALL_DIR=/opt/odooboat/python \
    UV_PYTHON_PREFERENCE=only-managed \
    UV_NO_CACHE=1 \
    UV_LINK_MODE=copy

# System libraries for the compiled requirements, fonts, tini, and the
# wkhtmltopdf build selected by the matrix, verified against a pinned digest.
COPY wkhtmltopdf.sha256 /tmp/wkhtmltopdf.sha256
RUN set -eux; \
    test -n "$SERIES" && test -n "$WKHTMLTOPDF_VERSION" && test -n "$WKHTMLTOPDF_BUILD"; \
    deb="wkhtmltox_${WKHTMLTOPDF_VERSION}.${WKHTMLTOPDF_BUILD}_${TARGETARCH}.deb"; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl; \
    curl -fsSL --retry 3 -o "/tmp/$deb" \
        "https://github.com/wkhtmltopdf/packaging/releases/download/${WKHTMLTOPDF_VERSION}/$deb"; \
    (cd /tmp && grep -F "  $deb" wkhtmltopdf.sha256 | sha256sum --check --strict -); \
    apt-get install -y --no-install-recommends \
        "/tmp/$deb" tini \
        libpq5 libldap-2.5-0 libsasl2-2 libxml2 libxslt1.1 \
        libjpeg62-turbo zlib1g libfreetype6 liblcms2-2 libwebp7 libffi8 libssl3 \
        fonts-dejavu-core fonts-noto-core fonts-noto-cjk; \
    apt-get purge -y --auto-remove curl; \
    rm -rf /var/lib/apt/lists/* /tmp/*; \
    wkhtmltopdf --version | grep -F "wkhtmltopdf ${WKHTMLTOPDF_VERSION%-*} (with patched qt)"

COPY --from=builder /opt/odooboat/python /opt/odooboat/python
COPY --from=builder /opt/odooboat/venv /opt/odooboat/venv
COPY --from=builder /opt/odooboat/base /opt/odooboat/base
COPY --from=uv /uv /opt/odooboat/bin/uv
COPY --chmod=0755 entrypoint /opt/odooboat/bin/entrypoint

# Execution user and fixed locations. The image runs as UID 1000 by default
# and supports any UID whose group is 0: the data directory is group 0 and
# group-writable, so Odooboat can run Odoo as the host user when sources are
# bind-mounted on Linux.
#
# Build checks: the Python version matches the matrix, odoo is not importable
# from site-packages (ADR 0008 §3), and every compiled requirement finds its
# system libraries; reports can render barcodes as Odoo does.
RUN set -eux; \
    groupadd --gid 1000 odoo; \
    useradd --uid 1000 --gid 1000 --home-dir /var/lib/odoo --no-create-home \
        --shell /usr/sbin/nologin odoo; \
    mkdir -p /var/lib/odoo /opt/odoo/community /opt/odoo/enterprise; \
    chown 1000:0 /var/lib/odoo; \
    chmod 2775 /var/lib/odoo; \
    python -c "import sys; v = '%d.%d' % sys.version_info[:2]; sys.exit(0 if v == '${PYTHON}' else 'python ' + v + ' != ${PYTHON}')"; \
    python -c "import importlib.util, sys; sys.exit('odoo is importable' if importlib.util.find_spec('odoo') else 0)"; \
    python -c "import lxml.etree, PIL.Image, psycopg2, ldap, gevent, greenlet, sass, cryptography.hazmat.bindings.openssl.binding, psutil"; \
    python -c "from reportlab.graphics.barcode import createBarcodeDrawing; createBarcodeDrawing('Code128', value='odooboat').asString('png')"

USER 1000:1000
WORKDIR /var/lib/odoo
EXPOSE 8069 8072
ENTRYPOINT ["/usr/bin/tini", "--", "/opt/odooboat/bin/entrypoint"]
CMD ["odoo"]
