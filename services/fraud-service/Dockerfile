
# ---------- build stage ----------
FROM python:3.13-slim AS build

# uv from its official image, pinned. No "pip install uv" needed.
COPY --from=ghcr.io/astral-sh/uv:0.12.23 /uv /usr/local/bin/uv

# Copy files from uv's cache (no hardlink attempts), precompile installed
# packages to bytecode, never download a different Python, and put the
# virtual environment at a fixed path the runtime stage expects.
ENV UV_LINK_MODE=copy \
    UV_COMPILE_BYTECODE=1 \
    UV_PYTHON_DOWNLOADS=never \
    UV_PROJECT_ENVIRONMENT=/app/.venv

WORKDIR /app

# Dependencies first: cached until pyproject.toml or uv.lock change.
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev

# Application code and the model artifact, compiled to bytecode now
# because the runtime filesystem is read-only.
COPY app/ app/
COPY model/ model/
RUN python -m compileall -q app

# ---------- runtime stage ----------
# Must be the same base as the build stage: the venv links to its python.
FROM python:3.13-slim

RUN groupadd --system --gid 10001 app \
 && useradd --system --uid 10001 --gid app --no-create-home --shell /usr/sbin/nologin app

WORKDIR /app
COPY --from=build /app /app

ENV PATH="/app/.venv/bin:$PATH" \
    PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1

USER 10001:10001
EXPOSE 8086
ENTRYPOINT ["python", "-m", "app"]