# ---- build: install locked dependencies into a virtualenv ----
FROM python:3.13-slim AS build
COPY --from=ghcr.io/astral-sh/uv:0.12.23 /uv /usr/local/bin/uv
ENV UV_LINK_MODE=copy UV_COMPILE_BYTECODE=1 UV_PYTHON_DOWNLOADS=never UV_PROJECT_ENVIRONMENT=/app/.venv
WORKDIR /app
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev
COPY app/ app/
COPY migrations/ migrations/
RUN python -m compileall -q app

# ---- runtime: same base image, no uv, no build tools, non-root ----
FROM python:3.13-slim
RUN groupadd --system --gid 10001 app && useradd --system --uid 10001 --gid app --no-create-home --shell /usr/sbin/nologin app
WORKDIR /app
COPY --from=build /app /app
ENV PATH="/app/.venv/bin:$PATH" PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
USER 10001:10001
EXPOSE 8085
# "python -m app" serves the API; "python -m app migrate" runs the migration Job.
ENTRYPOINT ["python", "-m", "app"]