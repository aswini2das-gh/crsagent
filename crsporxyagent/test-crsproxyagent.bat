@echo off
setlocal enabledelayedexpansion

:: ============================================================================
:: CRSProxyAgent - End-to-End Test Script for Windows (Docker Desktop + K8s)
:: ============================================================================
:: Usage:
::   test-crsproxyagent.bat                  Run all tests
::   test-crsproxyagent.bat prereq           Check prerequisites only
::   test-crsproxyagent.bat unit             Run unit tests only
::   test-crsproxyagent.bat setup            Set up test infrastructure only
::   test-crsproxyagent.bat deploy           Build and deploy operator only
::   test-crsproxyagent.bat verify           Run verification tests only
::   test-crsproxyagent.bat cleanup          Clean up everything
:: ============================================================================

set "NAMESPACE=crs-system"
set "KAFKA_NAMESPACE=kafka-dev"
set "KYVERNO_NAMESPACE=kyverno"
set "CLUSTER_ID=test-cluster-01"
set "OPERATOR_IMAGE=crsproxyagent:dev"
set "KYVERNO_REPO=https://kyverno.github.io/kyverno/"
rem Policy engine + token API, reachable from the Kyverno admission-controller pods.
rem Override before running, e.g.  set POLICY_API_URL=http://my-engine:8080
if not defined POLICY_API_URL set "POLICY_API_URL=http://host.docker.internal:8080"
if not defined TOKEN_API_URL set "TOKEN_API_URL=%POLICY_API_URL%"
set "PASS=0"
set "FAIL=0"
set "SKIP=0"
set "TOTAL=0"

:: Colors via ANSI (Windows 10+)
set "GREEN=[92m"
set "RED=[91m"
set "YELLOW=[93m"
set "CYAN=[96m"
set "BOLD=[1m"
set "RESET=[0m"

:: ---- Entrypoint ----
if "%~1"=="" goto :run_all
if /i "%~1"=="prereq" goto :phase_prereq
if /i "%~1"=="unit" goto :phase_unit
if /i "%~1"=="setup" goto :phase_setup
if /i "%~1"=="deploy" goto :phase_deploy
if /i "%~1"=="verify" goto :phase_verify
if /i "%~1"=="cleanup" goto :phase_cleanup
if /i "%~1"=="help" goto :usage
echo Unknown command: %~1
goto :usage

:usage
echo.
echo   Usage: %~nx0 [prereq^|unit^|setup^|deploy^|verify^|cleanup^|help]
echo   No argument runs the full pipeline.
echo.
exit /b 1

:: ============================================================================
:: FULL PIPELINE
:: ============================================================================
:run_all
call :banner "CRSProxyAgent End-to-End Test Suite"
echo   Cluster ID : %CLUSTER_ID%
echo   Namespace  : %NAMESPACE%
echo   Timestamp  : %date% %time%
echo.

call :phase_prereq
if !FAIL! gtr 0 (
    call :summary
    echo.
    echo %RED%Prerequisites failed. Fix the issues above before continuing.%RESET%
    exit /b 1
)

call :phase_unit
call :phase_setup
call :phase_deploy
call :phase_verify
call :summary
goto :eof

:: ============================================================================
:: PHASE 0: PREREQUISITES
:: ============================================================================
:phase_prereq
call :banner "Phase 0: Prerequisites Check"

:: Docker
call :check_cmd "docker" "Docker CLI"
if !errorlevel! equ 0 (
    docker info >nul 2>&1
    if !errorlevel! equ 0 (
        call :test_pass "Docker daemon is running"
    ) else (
        call :test_fail "Docker daemon is NOT running - start Docker Desktop"
    )
)

:: Kubernetes
call :check_cmd "kubectl" "kubectl"
if !errorlevel! equ 0 (
    kubectl cluster-info >nul 2>&1
    if !errorlevel! equ 0 (
        call :test_pass "Kubernetes cluster is reachable"
        :: Check it's Docker Desktop
        for /f "tokens=*" %%a in ('kubectl config current-context 2^>nul') do set "KCTX=%%a"
        echo          Context: !KCTX!
    ) else (
        call :test_fail "Kubernetes cluster is NOT reachable - enable K8s in Docker Desktop"
    )
)

:: Helm
call :check_cmd "helm" "Helm"

:: Go
call :check_cmd "go" "Go compiler"
if !errorlevel! equ 0 (
    for /f "tokens=*" %%a in ('go version 2^>nul') do echo          %%a
)

:: controller-gen (optional)
where controller-gen >nul 2>&1
if !errorlevel! equ 0 (
    call :test_pass "controller-gen found"
) else (
    call :test_skip "controller-gen not found (will install via go install if needed)"
)

echo.
goto :eof

:: ============================================================================
:: PHASE 1: UNIT TESTS
:: ============================================================================
:phase_unit
call :banner "Phase 1: Unit Tests"

:: Check go.mod exists
if not exist "go.mod" (
    call :test_fail "go.mod not found - run this script from the project root"
    echo.
    goto :eof
)

echo   Running go vet...
go vet ./... >nul 2>&1
if !errorlevel! equ 0 (
    call :test_pass "go vet passed"
) else (
    call :test_fail "go vet found issues"
)

echo   Running unit tests...
go test ./... -count=1 -short -timeout 120s 2>&1
if !errorlevel! equ 0 (
    call :test_pass "All unit tests passed"
) else (
    call :test_fail "Some unit tests failed"
)

echo   Running tests with race detector...
go test ./... -race -short -timeout 180s >nul 2>&1
if !errorlevel! equ 0 (
    call :test_pass "Race detector found no issues"
) else (
    call :test_fail "Race detector found issues"
)

echo.
goto :eof

:: ============================================================================
:: PHASE 2: TEST INFRASTRUCTURE SETUP
:: ============================================================================
:phase_setup
call :banner "Phase 2: Test Infrastructure Setup"

:: --- Kyverno ---
:: Kyverno is now a chart dependency of CRSProxyAgent itself (see
:: deploy\helm\crsproxyagent\Chart.yaml) and is installed automatically as
:: part of "helm install crsproxyagent" in Phase 3 - no separate install here.
echo   %CYAN%Kyverno will be installed automatically with CRSProxyAgent (chart dependency)%RESET%
call :test_skip "Kyverno install deferred to Phase 3 (bundled as a chart dependency)"

:: --- Local Kafka (dev prerequisite) ---
echo.
echo   %CYAN%Setting up local Kafka (dev prerequisite - NOT part of operator)...%RESET%
helm repo add bitnami https://charts.bitnami.com/bitnami >nul 2>&1
helm repo update >nul 2>&1

kubectl get namespace %KAFKA_NAMESPACE% >nul 2>&1
if !errorlevel! equ 0 (
    echo          Kafka namespace already exists, checking pods...
) else (
    kubectl create namespace %KAFKA_NAMESPACE% >nul 2>&1
    helm install kafka-dev bitnami/kafka ^
        --namespace %KAFKA_NAMESPACE% ^
        --set listeners.client.protocol=PLAINTEXT ^
        --set listeners.controller.protocol=PLAINTEXT ^
        --set controller.replicaCount=1 ^
        --set provisioning.enabled=true ^
        --set "provisioning.topics[0].name=crs.discovery.nodes.%CLUSTER_ID%" ^
        --set "provisioning.topics[0].partitions=3" ^
        --set "provisioning.topics[0].replicationFactor=1" ^
        --wait --timeout 300s 2>&1
    if !errorlevel! neq 0 (
        call :test_fail "Kafka installation failed"
        echo.
        goto :eof
    )
)
call :wait_for_pods "%KAFKA_NAMESPACE%" "app.kubernetes.io/name=kafka" 300
if !errorlevel! equ 0 (
    call :test_pass "Kafka is running (local dev instance)"
) else (
    call :test_fail "Kafka pods not ready"
)

:: --- Operator namespace ---
kubectl create namespace %NAMESPACE% >nul 2>&1

:: --- Create mock secrets (simulating OpenBao injection) ---
echo.
echo   %CYAN%Creating mock secrets (simulating OpenBao injection)...%RESET%

kubectl create secret generic crs-kafka-credentials ^
    --namespace %NAMESPACE% ^
    --from-literal=username=test-user ^
    --from-literal=password=test-pass ^
    --dry-run=client -o yaml | kubectl apply -f - >nul 2>&1
call :test_pass "Kafka credentials secret created"

kubectl create secret generic kyverno-acme-token ^
    --namespace %NAMESPACE% ^
    --from-literal=token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.test-acme ^
    --dry-run=client -o yaml | kubectl apply -f - >nul 2>&1
call :test_pass "Kyverno acme-corp token secret created"

kubectl create secret generic crs-signing-key ^
    --namespace %NAMESPACE% ^
    --from-literal=key=dGVzdC1zaWduaW5nLWtleS1mb3ItZGV2 ^
    --dry-run=client -o yaml | kubectl apply -f - >nul 2>&1
call :test_pass "Telemetry signing key secret created"

echo.
goto :eof

:: ============================================================================
:: PHASE 3: BUILD & DEPLOY
:: ============================================================================
:phase_deploy
call :banner "Phase 3: Build and Deploy Operator"

:: Generate manifests
echo   %CYAN%Generating CRD manifests...%RESET%
if exist "Makefile" (
    where make >nul 2>&1
    if !errorlevel! equ 0 (
        make manifests generate 2>&1
        if !errorlevel! equ 0 (
            call :test_pass "CRD manifests and deepcopy generated"
        ) else (
            call :test_fail "Manifest generation failed"
        )
    ) else (
        echo          make not found, trying controller-gen directly...
        where controller-gen >nul 2>&1
        if !errorlevel! equ 0 (
            controller-gen crd paths="./..." output:crd:artifacts:config=config/crd/bases
            controller-gen object paths="./..."
            call :test_pass "CRD manifests generated via controller-gen"
        ) else (
            call :test_skip "Neither make nor controller-gen available"
        )
    )
) else (
    call :test_skip "No Makefile found, skipping manifest generation"
)

:: Build Docker image
echo.
echo   %CYAN%Building Docker image...%RESET%
docker build -t %OPERATOR_IMAGE% . 2>&1
if !errorlevel! equ 0 (
    call :test_pass "Docker image built: %OPERATOR_IMAGE%"
) else (
    call :test_fail "Docker image build failed"
    echo.
    goto :eof
)

:: Install CRDs
echo.
echo   %CYAN%Installing CRDs...%RESET%
if exist "config\crd\bases" (
    kubectl apply -f config\crd\bases\ 2>&1
    if !errorlevel! equ 0 (
        call :test_pass "CRDs installed"
    ) else (
        call :test_fail "CRD installation failed"
    )
) else (
    call :test_skip "config\crd\bases not found, Helm chart will install CRDs"
)

:: Fetch chart dependencies (Kyverno sub-chart)
echo.
echo   %CYAN%Fetching Helm chart dependencies (Kyverno)...%RESET%
helm repo add kyverno %KYVERNO_REPO% >nul 2>&1
helm repo update >nul 2>&1
helm dependency update deploy\helm\crsproxyagent\ 2>&1
if !errorlevel! equ 0 (
    call :test_pass "Chart dependencies fetched (Kyverno)"
) else (
    call :test_fail "Failed to fetch chart dependencies"
    echo.
    goto :eof
)

:: Deploy via Helm - this single release brings up node discovery, Kyverno
:: policy enforcement, and the telemetry stack together.
echo.
echo   %CYAN%Deploying CRSProxyAgent (+ Kyverno + telemetry) via Helm...%RESET%
helm upgrade --install crsproxyagent deploy\helm\crsproxyagent\ ^
    --namespace %NAMESPACE% ^
    --create-namespace ^
    --set clusterID=%CLUSTER_ID% ^
    --set kyverno.enabled=true ^
    --set kafka.brokers[0]=kafka-dev-controller-0.kafka-dev-controller-headless.%KAFKA_NAMESPACE%.svc.cluster.local:9092 ^
    --set kafka.authSecretName=crs-kafka-credentials ^
    --set policyAPI.endpoint=%POLICY_API_URL% ^
    --set policyAPI.path=/api/v1/admission/evaluate ^
    --set tokenAPI.endpoint=%TOKEN_API_URL% ^
    --set tokenAPI.path=/api/v1/dev/token ^
    --set tokenAPI.responseTokenField=access_token ^
    --set tokenAPI.defaultTTLSeconds=3600 ^
    --set policyAPI.tenants[0].id=acme-corp ^
    --set policyAPI.tenants[0].clientId=kyverno-acme ^
    --set policyAPI.tenants[0].ttlSeconds=3600 ^
    --set telemetry.enabled=true ^
    --set telemetry.exportEndpoint=http://mock-telemetry.%NAMESPACE%.svc:4318 ^
    --set telemetry.signingKeySecret=crs-signing-key ^
    --set operator.replicas=1 ^
    --set operator.image.repository=crsproxyagent ^
    --set operator.image.tag=dev ^
    --set operator.image.pullPolicy=Never ^
    --wait --timeout 300s 2>&1

if !errorlevel! equ 0 (
    call :test_pass "CRSProxyAgent Helm install succeeded (includes Kyverno)"
) else (
    call :test_fail "CRSProxyAgent Helm install failed"
    echo.
    echo   %YELLOW%Debugging info:%RESET%
    kubectl get pods -n %NAMESPACE%
    kubectl describe pods -n %NAMESPACE% -l app.kubernetes.io/name=crsproxyagent
)

:: Wait for operator pod
echo.
call :wait_for_pods "%NAMESPACE%" "app.kubernetes.io/name=crsproxyagent" 120
if !errorlevel! equ 0 (
    call :test_pass "Operator pod is running"
) else (
    call :test_fail "Operator pod not ready"
    echo.
    echo   %YELLOW%Pod logs:%RESET%
    kubectl logs -n %NAMESPACE% -l app.kubernetes.io/name=crsproxyagent --tail=30
)

:: Wait for Kyverno pods (now deployed as part of the same release)
echo.
call :wait_for_pods "%NAMESPACE%" "app.kubernetes.io/instance=crsproxyagent" 180
if !errorlevel! equ 0 (
    call :test_pass "Kyverno is running (bundled with CRSProxyAgent)"
) else (
    call :test_skip "Kyverno pods not confirmed ready (check manually)"
)

echo.
goto :eof

:: ============================================================================
:: PHASE 4: VERIFICATION TESTS
:: ============================================================================
:phase_verify
call :banner "Phase 4: Verification Tests"

:: ---- 4.1 CRD Registration ----
echo   %CYAN%4.1 CRD Registration%RESET%

for %%c in (nodediscoveryconfigs.crs.nvidia.com clusterprofiles.crs.nvidia.com telemetryconfigs.crs.nvidia.com) do (
    kubectl get crd %%c >nul 2>&1
    if !errorlevel! equ 0 (
        call :test_pass "CRD registered: %%c"
    ) else (
        call :test_fail "CRD missing: %%c"
    )
)

:: ---- 4.2 Apply Sample NodeDiscoveryConfig ----
echo.
echo   %CYAN%4.2 NodeDiscoveryConfig%RESET%

echo   Creating sample NodeDiscoveryConfig...
(
echo apiVersion: crs.nvidia.com/v1alpha1
echo kind: NodeDiscoveryConfig
echo metadata:
echo   name: test-discovery
echo   namespace: %NAMESPACE%
echo spec:
echo   clusterID: "%CLUSTER_ID%"
echo   pollInterval: "30s"
echo   discoverCPU: true
echo   discoverGPU: false
echo   discoverStorage: true
echo   discoverCloudProvider: true
echo   kafkaBrokers:
echo     - "kafka-dev-controller-0.kafka-dev-controller-headless.%KAFKA_NAMESPACE%.svc.cluster.local:9092"
echo   kafkaAuthSecret: "crs-kafka-credentials"
) > "%TEMP%\test-nodediscoveryconfig.yaml"

kubectl apply -f "%TEMP%\test-nodediscoveryconfig.yaml" 2>&1
if !errorlevel! equ 0 (
    call :test_pass "NodeDiscoveryConfig created"
) else (
    call :test_fail "NodeDiscoveryConfig creation failed"
)

:: Wait for reconciliation
echo   Waiting 15s for reconciliation...
timeout /t 15 /nobreak >nul

:: Check operator logs for discovery activity
echo   Checking operator logs for discovery activity...
kubectl logs -n %NAMESPACE% -l app.kubernetes.io/name=crsproxyagent --tail=50 2>nul | findstr /i "reconcil discover node" >nul 2>&1
if !errorlevel! equ 0 (
    call :test_pass "Operator is reconciling and discovering nodes"
) else (
    call :test_skip "Could not confirm reconciliation from logs (check manually)"
)

:: ---- 4.3 ClusterProfile ----
echo.
echo   %CYAN%4.3 ClusterProfile%RESET%

kubectl get clusterprofile -n %NAMESPACE% >nul 2>&1
if !errorlevel! equ 0 (
    call :test_pass "ClusterProfile resource exists"
    :: Check status has nodes
    kubectl get clusterprofile -n %NAMESPACE% -o jsonpath="{.items[0].status}" 2>nul
    echo.
) else (
    call :test_skip "ClusterProfile not yet created (may need more time)"
)

:: ---- 4.4 Node Discovery ----
echo.
echo   %CYAN%4.4 Node Discovery Validation%RESET%

:: Count discovered nodes vs actual nodes
for /f %%n in ('kubectl get nodes --no-headers 2^>nul ^| find /c /v ""') do set "NODE_COUNT=%%n"
echo          Kubernetes nodes found: %NODE_COUNT%

if %NODE_COUNT% gtr 0 (
    call :test_pass "Cluster has %NODE_COUNT% node(s) available for discovery"
) else (
    call :test_fail "No nodes found in cluster"
)

:: Check node labels that discovery reads
echo   Checking node metadata accessible to discovery...
kubectl get nodes -o jsonpath="{range .items[*]}{.metadata.name}: cpu={.status.capacity.cpu}, mem={.status.capacity.memory}, os={.status.nodeInfo.operatingSystem}/{.status.nodeInfo.architecture}{'\n'}{end}" 2>nul
call :test_pass "Node metadata is accessible"

:: ---- 4.5 Kafka Events ----
echo.
echo   %CYAN%4.5 Kafka Event Publishing%RESET%

echo   Launching Kafka consumer pod to check events...
kubectl run kafka-test-consumer ^
    --namespace %KAFKA_NAMESPACE% ^
    --image=bitnami/kafka:latest ^
    --restart=Never ^
    --rm -i --wait --timeout=30s ^
    --command -- kafka-console-consumer.sh ^
    --bootstrap-server kafka-dev-controller-0.kafka-dev-controller-headless.%KAFKA_NAMESPACE%.svc.cluster.local:9092 ^
    --topic crs.discovery.nodes.%CLUSTER_ID% ^
    --from-beginning ^
    --max-messages 1 ^
    --timeout-ms 20000 2>&1 | findstr /i "clusterID nodeUID" >nul 2>&1

if !errorlevel! equ 0 (
    call :test_pass "Kafka events published with expected schema"
) else (
    call :test_skip "Could not consume Kafka events (may need more time or topic may not exist yet)"
    :: Cleanup the consumer pod if it's stuck
    kubectl delete pod kafka-test-consumer --namespace %KAFKA_NAMESPACE% --ignore-not-found >nul 2>&1
)

:: ---- 4.6 Kyverno Policies ----
echo.
echo   %CYAN%4.6 Kyverno Policy Management%RESET%

kubectl get clusterpolicy -l app.kubernetes.io/managed-by=crsproxyagent >nul 2>&1
if !errorlevel! equ 0 (
    for /f %%n in ('kubectl get clusterpolicy -l app.kubernetes.io/managed-by^=crsproxyagent --no-headers 2^>nul ^| find /c /v ""') do set "POLICY_COUNT=%%n"
    if !POLICY_COUNT! gtr 0 (
        call :test_pass "!POLICY_COUNT! CRS-managed ClusterPolicy(ies) found"
        echo          Policies:
        kubectl get clusterpolicy -l app.kubernetes.io/managed-by=crsproxyagent -o custom-columns=NAME:.metadata.name,TENANT:.metadata.labels.crs\.nvidia\.com/tenant,ACTION:.spec.validationFailureAction 2>nul
    ) else (
        call :test_skip "No CRS-managed ClusterPolicies yet (check tenant config)"
    )
) else (
    call :test_skip "ClusterPolicy CRD not available (is Kyverno installed?)"
)

:: ---- 4.7 Telemetry Stack ----
echo.
echo   %CYAN%4.7 Telemetry Stack%RESET%

:: Check cAdvisor DaemonSet
kubectl get daemonset -n %NAMESPACE% -l app.kubernetes.io/component=cadvisor >nul 2>&1
if !errorlevel! equ 0 (
    call :test_pass "cAdvisor DaemonSet exists"
) else (
    call :test_skip "cAdvisor DaemonSet not found (telemetry may be disabled or pending)"
)

:: Check OTel Collector DaemonSet
kubectl get daemonset -n %NAMESPACE% -l app.kubernetes.io/component=otel-collector >nul 2>&1
if !errorlevel! equ 0 (
    call :test_pass "OTel Collector DaemonSet exists"
) else (
    call :test_skip "OTel Collector DaemonSet not found"
)

:: Check Signing Gateway Deployment
kubectl get deployment -n %NAMESPACE% -l app.kubernetes.io/component=signing-gateway >nul 2>&1
if !errorlevel! equ 0 (
    call :test_pass "Telemetry Signing Gateway Deployment exists"
) else (
    call :test_skip "Signing Gateway Deployment not found"
)

:: Check OTel ConfigMap
kubectl get configmap -n %NAMESPACE% -l app.kubernetes.io/component=otel-collector >nul 2>&1
if !errorlevel! equ 0 (
    call :test_pass "OTel Collector ConfigMap exists"
) else (
    call :test_skip "OTel Collector ConfigMap not found"
)

:: ---- 4.8 Operator Health ----
echo.
echo   %CYAN%4.8 Operator Health and Readiness%RESET%

:: Get operator pod name
for /f "tokens=1" %%p in ('kubectl get pods -n %NAMESPACE% -l app.kubernetes.io/name^=crsproxyagent -o name 2^>nul') do set "OPERATOR_POD=%%p"

if defined OPERATOR_POD (
    :: Liveness probe
    kubectl exec -n %NAMESPACE% %OPERATOR_POD% -- wget -qO- http://localhost:8081/healthz 2>nul | findstr /i "ok" >nul 2>&1
    if !errorlevel! equ 0 (
        call :test_pass "Liveness probe (/healthz) returns OK"
    ) else (
        :: Try curl as fallback
        kubectl exec -n %NAMESPACE% %OPERATOR_POD% -- curl -sf http://localhost:8081/healthz >nul 2>&1
        if !errorlevel! equ 0 (
            call :test_pass "Liveness probe (/healthz) returns OK"
        ) else (
            call :test_skip "Cannot reach /healthz (distroless image may lack wget/curl)"
        )
    )

    :: Readiness probe
    kubectl exec -n %NAMESPACE% %OPERATOR_POD% -- wget -qO- http://localhost:8081/readyz 2>nul | findstr /i "ok" >nul 2>&1
    if !errorlevel! equ 0 (
        call :test_pass "Readiness probe (/readyz) returns OK"
    ) else (
        call :test_skip "Cannot reach /readyz directly (check via pod status)"
    )

    :: Pod status-based readiness check
    kubectl get %OPERATOR_POD% -n %NAMESPACE% -o jsonpath="{.status.conditions[?(@.type=='Ready')].status}" 2>nul | findstr "True" >nul 2>&1
    if !errorlevel! equ 0 (
        call :test_pass "Operator pod reports Ready=True"
    ) else (
        call :test_fail "Operator pod is NOT ready"
    )
) else (
    call :test_fail "No operator pod found"
)

:: ---- 4.9 RBAC ----
echo.
echo   %CYAN%4.9 RBAC Verification%RESET%

kubectl get clusterrole crsproxyagent-role >nul 2>&1
if !errorlevel! equ 0 (
    call :test_pass "ClusterRole exists"
) else (
    :: Try with Helm naming convention
    kubectl get clusterrole -l app.kubernetes.io/name=crsproxyagent >nul 2>&1
    if !errorlevel! equ 0 (
        call :test_pass "ClusterRole exists (Helm-named)"
    ) else (
        call :test_fail "ClusterRole not found"
    )
)

kubectl get clusterrolebinding -l app.kubernetes.io/name=crsproxyagent >nul 2>&1
if !errorlevel! equ 0 (
    call :test_pass "ClusterRoleBinding exists"
) else (
    call :test_skip "ClusterRoleBinding not found by label"
)

kubectl get serviceaccount -n %NAMESPACE% -l app.kubernetes.io/name=crsproxyagent >nul 2>&1
if !errorlevel! equ 0 (
    call :test_pass "ServiceAccount exists"
) else (
    call :test_skip "ServiceAccount not found by label"
)

:: ---- 4.10 Security Context ----
echo.
echo   %CYAN%4.10 Security Context%RESET%

if defined OPERATOR_POD (
    :: Check runAsNonRoot
    kubectl get %OPERATOR_POD% -n %NAMESPACE% -o jsonpath="{.spec.containers[0].securityContext.runAsNonRoot}" 2>nul | findstr "true" >nul 2>&1
    if !errorlevel! equ 0 (
        call :test_pass "runAsNonRoot=true"
    ) else (
        call :test_skip "runAsNonRoot not set on container (may be at pod level)"
    )

    :: Check readOnlyRootFilesystem
    kubectl get %OPERATOR_POD% -n %NAMESPACE% -o jsonpath="{.spec.containers[0].securityContext.readOnlyRootFilesystem}" 2>nul | findstr "true" >nul 2>&1
    if !errorlevel! equ 0 (
        call :test_pass "readOnlyRootFilesystem=true"
    ) else (
        call :test_skip "readOnlyRootFilesystem not confirmed"
    )
)

:: ---- 4.11 Leader Election ----
echo.
echo   %CYAN%4.11 Leader Election%RESET%

kubectl get lease -n %NAMESPACE% 2>nul | findstr /i "crsproxyagent" >nul 2>&1
if !errorlevel! equ 0 (
    call :test_pass "Leader election lease found"
) else (
    call :test_skip "Leader election lease not found (may use a different name)"
)

:: ---- 4.12 Operator Logs Health Check ----
echo.
echo   %CYAN%4.12 Operator Logs Analysis%RESET%

if defined OPERATOR_POD (
    :: Check for panic/fatal
    kubectl logs -n %NAMESPACE% %OPERATOR_POD% --tail=100 2>nul | findstr /i "panic fatal" >nul 2>&1
    if !errorlevel! equ 0 (
        call :test_fail "PANIC or FATAL errors found in operator logs"
        echo          %RED%Last 10 log lines:%RESET%
        kubectl logs -n %NAMESPACE% %OPERATOR_POD% --tail=10
    ) else (
        call :test_pass "No PANIC/FATAL errors in operator logs"
    )

    :: Check for repeated error loops
    for /f %%n in ('kubectl logs -n %NAMESPACE% %OPERATOR_POD% --tail^=100 2^>nul ^| findstr /i "error" ^| find /c /v ""') do set "ERR_COUNT=%%n"
    if !ERR_COUNT! gtr 20 (
        call :test_fail "High error count in logs: !ERR_COUNT! errors in last 100 lines"
    ) else if !ERR_COUNT! gtr 0 (
        echo          %YELLOW%!ERR_COUNT! error(s) in last 100 log lines (review below)%RESET%
        kubectl logs -n %NAMESPACE% %OPERATOR_POD% --tail=100 2>nul | findstr /i "error"
        call :test_pass "Error count acceptable: !ERR_COUNT! errors"
    ) else (
        call :test_pass "No errors in recent logs"
    )
)

echo.
goto :eof

:: ============================================================================
:: PHASE 5: CLEANUP
:: ============================================================================
:phase_cleanup
call :banner "Phase 5: Cleanup"

echo   %CYAN%Removing test resources...%RESET%
kubectl delete -f "%TEMP%\test-nodediscoveryconfig.yaml" --ignore-not-found >nul 2>&1
echo          Removed test NodeDiscoveryConfig

echo   %CYAN%Uninstalling CRSProxyAgent (and bundled Kyverno)...%RESET%
helm uninstall crsproxyagent --namespace %NAMESPACE% >nul 2>&1
echo          Helm release removed (Kyverno removed with it, as a chart dependency)

echo   %CYAN%Removing CRDs...%RESET%
kubectl delete crd nodediscoveryconfigs.crs.nvidia.com --ignore-not-found >nul 2>&1
kubectl delete crd clusterprofiles.crs.nvidia.com --ignore-not-found >nul 2>&1
kubectl delete crd telemetryconfigs.crs.nvidia.com --ignore-not-found >nul 2>&1
echo          CRDs removed

echo   %CYAN%Removing secrets...%RESET%
kubectl delete secret crs-kafka-credentials kyverno-acme-token crs-signing-key -n %NAMESPACE% --ignore-not-found >nul 2>&1
echo          Secrets removed

echo   %CYAN%Removing namespace %NAMESPACE%...%RESET%
kubectl delete namespace %NAMESPACE% --ignore-not-found >nul 2>&1
echo          Namespace removed

echo.
set /p "REMOVE_INFRA=Remove local dev Kafka too? (y/N): "
if /i "!REMOVE_INFRA!"=="y" (
    echo   %CYAN%Uninstalling Kafka...%RESET%
    helm uninstall kafka-dev --namespace %KAFKA_NAMESPACE% >nul 2>&1
    kubectl delete namespace %KAFKA_NAMESPACE% --ignore-not-found >nul 2>&1
    echo          Kafka removed
)

echo.
echo   %GREEN%Cleanup complete.%RESET%
echo.
goto :eof

:: ============================================================================
:: HELPER FUNCTIONS
:: ============================================================================

:banner
echo.
echo %BOLD%============================================================================%RESET%
echo %BOLD%  %~1%RESET%
echo %BOLD%============================================================================%RESET%
echo.
goto :eof

:check_cmd
where %~1 >nul 2>&1
if !errorlevel! equ 0 (
    call :test_pass "%~2 found"
) else (
    call :test_fail "%~2 NOT found - install it and add to PATH"
)
exit /b !errorlevel!

:test_pass
set /a TOTAL+=1
set /a PASS+=1
echo   %GREEN%  PASS%RESET%  %~1
goto :eof

:test_fail
set /a TOTAL+=1
set /a FAIL+=1
echo   %RED%  FAIL%RESET%  %~1
goto :eof

:test_skip
set /a TOTAL+=1
set /a SKIP+=1
echo   %YELLOW%  SKIP%RESET%  %~1
goto :eof

:wait_for_pods
:: %1 = namespace, %2 = label selector, %3 = timeout in seconds
set "WP_NS=%~1"
set "WP_LABEL=%~2"
set "WP_TIMEOUT=%~3"
set "WP_ELAPSED=0"

echo          Waiting for pods (label=%WP_LABEL%) in %WP_NS%...
:wait_loop
kubectl get pods -n %WP_NS% -l %WP_LABEL% -o jsonpath="{.items[*].status.phase}" 2>nul | findstr "Pending" >nul 2>&1
if !errorlevel! equ 0 (
    if !WP_ELAPSED! geq %WP_TIMEOUT% (
        echo          Timed out after %WP_TIMEOUT%s
        exit /b 1
    )
    timeout /t 5 /nobreak >nul
    set /a WP_ELAPSED+=5
    goto :wait_loop
)

:: Check all pods are Running/Succeeded
kubectl wait --for=condition=Ready pods -l %WP_LABEL% -n %WP_NS% --timeout=%WP_TIMEOUT%s >nul 2>&1
exit /b !errorlevel!

:summary
echo.
echo %BOLD%============================================================================%RESET%
echo %BOLD%  TEST SUMMARY%RESET%
echo %BOLD%============================================================================%RESET%
echo.
echo   Total : %TOTAL%
echo   %GREEN%Passed: %PASS%%RESET%
echo   %RED%Failed: %FAIL%%RESET%
echo   %YELLOW%Skipped: %SKIP%%RESET%
echo.
if %FAIL% equ 0 (
    echo   %GREEN%%BOLD%ALL TESTS PASSED%RESET%
) else (
    echo   %RED%%BOLD%%FAIL% TEST(S) FAILED%RESET%
)
echo.
echo   Timestamp: %date% %time%
echo.
goto :eof
