<#
.SYNOPSIS
  Sobe o Postgres, compila e roda o razao.

.EXAMPLE
  .\run.ps1              # sobe tudo e abre http://127.0.0.1:7878 (modo demonstração)
  .\run.ps1 -Dev         # backend + Vite com hot reload em http://localhost:5174
  .\run.ps1 -Test        # go vet, testes (unidade + integração no Postgres) e tipos do front
  .\run.ps1 -Bench       # benchmark: sem agrupar x commit em grupo
  .\run.ps1 -Down        # para o Postgres (os dados ficam no volume)
  .\run.ps1 -NoDemo      # sem seed, teste de carga e adulteração pela interface
#>
param(
    [switch]$Dev,
    [switch]$Test,
    [switch]$Bench,
    [switch]$Down,
    [switch]$NoDemo,
    [switch]$NoBrowser,
    [int]$Port = 7878
)

$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
Set-Location $root

function Step($msg) { Write-Host "`n==> $msg" -ForegroundColor Green }
function Need($cmd, $hint) {
    if (-not (Get-Command $cmd -ErrorAction SilentlyContinue)) {
        Write-Host "Faltando: $cmd. $hint" -ForegroundColor Red
        exit 1
    }
}

Need docker 'Instale o Docker Desktop: https://www.docker.com/products/docker-desktop/'

if ($Down) {
    docker compose down
    exit $LASTEXITCODE
}

Need go   'Instale Go 1.26+ em https://go.dev/dl/'
Need node 'Instale Node 22+ em https://nodejs.org/'

# --- Docker e Postgres -----------------------------------------------------
function DockerUp { docker info *> $null; return $LASTEXITCODE -eq 0 }

if (-not (DockerUp)) {
    $desktop = Join-Path $env:ProgramFiles 'Docker\Docker\Docker Desktop.exe'
    if (-not (Test-Path $desktop)) {
        Write-Host 'O Docker não está rodando e não achei o Docker Desktop para iniciar.' -ForegroundColor Red
        exit 1
    }
    Step 'Iniciando o Docker Desktop (pode levar um minuto)'
    Start-Process $desktop
    for ($i = 0; $i -lt 90 -and -not (DockerUp); $i++) { Start-Sleep -Seconds 2 }
    if (-not (DockerUp)) {
        Write-Host 'O Docker não ficou pronto a tempo. Abra o Docker Desktop e rode de novo.' -ForegroundColor Red
        exit 1
    }
}

Step 'Postgres (docker compose)'
docker compose up -d --wait
if ($LASTEXITCODE) { throw 'não foi possível subir o Postgres' }

# --- Frontend --------------------------------------------------------------
Push-Location web
try {
    if (-not (Test-Path node_modules)) {
        Step 'Instalando dependências do frontend'
        npm install --no-audit --no-fund
        if ($LASTEXITCODE) { throw 'npm install falhou' }
    }
} finally { Pop-Location }

if ($Test) {
    Step 'Testes do Go (unidade + integração)'
    go vet ./...
    if ($LASTEXITCODE) { exit $LASTEXITCODE }
    go test ./...
    if ($LASTEXITCODE) { exit $LASTEXITCODE }
    Step 'Checagem de tipos do frontend'
    Push-Location web; npm run typecheck; $code = $LASTEXITCODE; Pop-Location
    exit $code
}

$serveArgs = @('serve', '-addr', "127.0.0.1:$Port")
if (-not $NoDemo) { $serveArgs += '-demo' }

if ($Dev) {
    Step "Backend em http://127.0.0.1:$Port (janela separada)"
    $backend = Start-Process -PassThru -FilePath go -ArgumentList (@('run', './cmd/razao') + $serveArgs) -WorkingDirectory $root
    try {
        Step 'Frontend com hot reload em http://localhost:5174'
        if (-not $NoBrowser) { Start-Process 'http://localhost:5174' }
        Push-Location web
        npm run dev
    } finally {
        Pop-Location
        # go run cria um processo filho: derruba a árvore inteira.
        if ($backend -and -not $backend.HasExited) { taskkill /PID $backend.Id /T /F | Out-Null }
    }
    exit 0
}

Step 'Compilando o frontend'
Push-Location web
try {
    npm run build
    if ($LASTEXITCODE) { throw 'build do frontend falhou' }
} finally { Pop-Location }

Step 'Compilando o binário (frontend embutido)'
New-Item -ItemType Directory -Force bin | Out-Null
go build -trimpath -ldflags '-s -w' -o bin/razao.exe ./cmd/razao
if ($LASTEXITCODE) { throw 'go build falhou' }

if ($Bench) {
    Step 'Benchmark'
    & "$root\bin\razao.exe" bench -seconds 10 -workers 1024 -compare
    exit $LASTEXITCODE
}

$url = "http://127.0.0.1:$Port"
Step "razao rodando em $url  (Ctrl+C para parar; o Postgres continua de pé, use .\run.ps1 -Down)"
if (-not $NoBrowser) {
    Start-Job -ScriptBlock {
        param($u)
        for ($i = 0; $i -lt 40; $i++) {
            try { Invoke-WebRequest "$u/healthz" -UseBasicParsing -TimeoutSec 1 | Out-Null; Start-Process $u; return } catch { Start-Sleep -Milliseconds 250 }
        }
    } -ArgumentList $url | Out-Null
}
& "$root\bin\razao.exe" @serveArgs
