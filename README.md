# Done and Drawn

A single-household manual task list with a character reward image generated after a task is completed. The Go service keeps task state and settings in SQLite, stores images on disk, and serves the Vue app and API from one origin.

## Requirements

- Go 1.22 or newer
- Node.js 20 or newer
- An OpenAI API key for reward image generation

## Run locally on Windows

From the repository root, create your local settings file and add your OpenAI API key once:

```powershell
if (-not (Test-Path .env)) { Copy-Item .env.example .env }
notepad .env
```

Start or restart the app with one command:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\start-local.ps1
```

Open http://127.0.0.1:8080/. The launcher builds the Vue app and Go server, stops the previous local instance, then starts the new one with the values from `.env`. For a quick restart without rebuilding, pass `-SkipBuild` to the same command.

The `.env` file is ignored by Git. The launcher passes the key only to the backend process after the frontend build finishes. It keeps SQLite and images under `data/` by default; back up that directory together. To change the key or model, edit `.env` and restart.

## Frontend development

Start the backend as above. In a second terminal, run:

```powershell
cd frontend
npm install
npm run dev
```

Open the URL printed by Vite. Its `/api` and `/media` requests are proxied to the Go backend. The local `.env` binds the Go server to this PC; the Raspberry Pi service below uses its own network address.

## Raspberry Pi service

Build on the Pi (or on another Linux ARM64 machine matching the Pi OS), then install the resulting binary at `/opt/image-todo/server`:

```sh
cd frontend && npm ci && npm run build
cd ..
mkdir -p build
go build -o build/server ./cmd/server
```

Create a system user and persistent data directory, install the binary and service unit, then put the backend key in `/etc/image-todo.env`:

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin image-todo
sudo install -d -o image-todo -g image-todo /opt/image-todo /var/lib/image-todo
sudo install -m 0755 build/server /opt/image-todo/server
sudo install -m 0644 deploy/image-todo.service /etc/systemd/system/image-todo.service
sudo install -m 0600 /dev/null /etc/image-todo.env
```

Add `OPENAI_API_KEY=...` and optionally `OPENAI_IMAGE_MODEL=...` to `/etc/image-todo.env`, then enable the service:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now image-todo
```

The unit binds to port 8080 on the Pi's interfaces. Only allow trusted household devices to reach it, and do not expose or forward that port to the internet.
