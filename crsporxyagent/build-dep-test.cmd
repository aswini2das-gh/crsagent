@echo off
rem ============================================================================
rem  build-dep-test.cmd
rem  End-to-end: build, then deploy, then test CRSProxyAgent + Kyverno admission.
rem
rem  Runs every step in order, prints PASS/FAIL per step (with the error output
rem  of a failing step), stops at the first failed BUILD/DEPLOY step, runs all
rem  TEST steps, and ends with a summary table. Exit code 0 = everything passed.
rem
rem  Usage:   build-dep-test.cmd
rem  Override any setting below by setting the variable first, e.g.
rem     set POLICY_API_URL=http://host.docker.internal:9090
rem     set SKIP_GO_TESTS=1
rem     set CLEANUP=1            (uninstall and delete test namespace at the end)
rem     set NO_COLOR=1
rem     set EXTRA_HELM_ARGS=-f my-values.yaml
rem  Logs: build-dep-test-logs\step-NN.log
rem ============================================================================
setlocal EnableExtensions EnableDelayedExpansion
cd /d "%~dp0"

rem ---------------------------- configuration ---------------------------------
if not defined CLUSTER_ID        set "CLUSTER_ID=dev-cluster-01"
if not defined RELEASE           set "RELEASE=crsproxyagent"
if not defined NAMESPACE         set "NAMESPACE=crs-system"
if not defined TEST_NS           set "TEST_NS=crs-demo"
if not defined IMG_REPO          set "IMG_REPO=crsproxyagent"
if not defined IMG_TAG           set "IMG_TAG=dev"
if not defined PULL_POLICY       set "PULL_POLICY=Never"
if not defined KYVERNO_REPO      set "KYVERNO_REPO=https://kyverno.github.io/kyverno/"
rem Policy engine + token API: as seen from this PC, and from inside the cluster.
if not defined ENGINE_HOST_URL   set "ENGINE_HOST_URL=http://localhost:8080"
if not defined POLICY_API_URL    set "POLICY_API_URL=http://host.docker.internal:8080"
if not defined TOKEN_API_URL     set "TOKEN_API_URL=%POLICY_API_URL%"
rem Tenant
if not defined TENANT_ID         set "TENANT_ID=acme-corp"
if not defined CLIENT_ID         set "CLIENT_ID=kyverno-acme"
if not defined SUBSCRIPTION_ID   set "SUBSCRIPTION_ID=sub-1001"
if not defined TTL_SECONDS       set "TTL_SECONDS=3600"
rem Sample pods
if not defined ALLOW_IMAGE       set "ALLOW_IMAGE=registry.crs.io/demo/app:1.0"
if not defined ALLOW_GPU_COUNT   set "ALLOW_GPU_COUNT=1"
if not defined ALLOW_GPU_TYPE    set "ALLOW_GPU_TYPE=A100"
if not defined DENY_IMAGE        set "DENY_IMAGE=docker.io/library/nginx:latest"
if not defined DENY_GPU_COUNT    set "DENY_GPU_COUNT=999"
rem Behaviour
if not defined SKIP_GO_TESTS     set "SKIP_GO_TESTS=0"
if not defined RECONCILE_INTERVAL set "RECONCILE_INTERVAL=30s"
if not defined TOKEN_CACHE_ENABLED set "TOKEN_CACHE_ENABLED=true"
if not defined WAIT_POLICY_SECS  set "WAIT_POLICY_SECS=150"
if not defined CLEANUP           set "CLEANUP=0"

set "IMG=%IMG_REPO%:%IMG_TAG%"
set "POLICY_NAME=crs-policy-%TENANT_ID%"
set "LOGDIR=%~dp0build-dep-test-logs"
set "SAMPLES=%~dp0samples"
set "ALLOW_YAML=%SAMPLES%\pod-allow.yaml"
set "DENY_YAML=%SAMPLES%\pod-deny.yaml"
set "POLICY_DUMP=%LOGDIR%\clusterpolicy.yaml"

rem ------------------------------- colours ------------------------------------
set "ESC=" & set "GREEN=" & set "RED=" & set "YELLOW=" & set "CYAN=" & set "RESET="
if not defined NO_COLOR (
  for /f %%E in ('echo prompt $E ^| cmd') do set "ESC=%%E"
)
if defined ESC (
  set "GREEN=%ESC%[32m" & set "RED=%ESC%[31m" & set "YELLOW=%ESC%[33m" & set "CYAN=%ESC%[36m" & set "RESET=%ESC%[0m"
)

rem ------------------------------- state --------------------------------------
set /a N=0
set /a FAILS=0
set "FATAL=1"
set "ABORT="
set "INSTALL_STARTED=0"
set "OPERATOR_DEPLOY=deployment/%RELEASE%"

if exist "%LOGDIR%" del /q "%LOGDIR%\*.log" "%LOGDIR%\*.yaml" >nul 2>&1
if not exist "%LOGDIR%" mkdir "%LOGDIR%"
if not exist "%SAMPLES%" mkdir "%SAMPLES%"
rem Sample pods are written up front into samples\ (never deleted), so they exist even if a later step fails.
(
  echo apiVersion: v1
  echo kind: Pod
  echo metadata:
  echo   name: gpu-ok
  echo   namespace: %TEST_NS%
  echo   annotations:
  echo     crs.io/subscription-id: %SUBSCRIPTION_ID%
  echo     crs.io/gpu-type: %ALLOW_GPU_TYPE%
  echo spec:
  echo   containers:
  echo     - name: app
  echo       image: %ALLOW_IMAGE%
  echo       ports:
  echo         - containerPort: %ALLOW_GPU_COUNT%
) >"%ALLOW_YAML%"
(
  echo apiVersion: v1
  echo kind: Pod
  echo metadata:
  echo   name: gpu-bad
  echo   namespace: %TEST_NS%
  echo   annotations:
  echo     crs.io/subscription-id: %SUBSCRIPTION_ID%
  echo     crs.io/gpu-type: %ALLOW_GPU_TYPE%
  echo spec:
  echo   containers:
  echo     - name: app
  echo       image: %DENY_IMAGE%
  echo       ports:
  echo         - containerPort: %DENY_GPU_COUNT%
) >"%DENY_YAML%"

echo.
echo !CYAN!=== CRSProxyAgent: build, deploy and test ===!RESET!
echo   cluster id    : %CLUSTER_ID%
echo   image         : %IMG%  ^(pullPolicy %PULL_POLICY%^)
echo   release / ns  : %RELEASE% / %NAMESPACE%
echo   policy API    : %POLICY_API_URL%
echo   token API     : %TOKEN_API_URL%
echo   tenant        : %TENANT_ID% ^(clientId %CLIENT_ID%^)
echo   test namespace: %TEST_NS%
echo   logs          : %LOGDIR%
echo   sample pods   : %SAMPLES%

rem ============================================================================
rem  PHASE 1 - PRE-FLIGHT
rem ============================================================================
call :phase "PHASE 1 - PRE-FLIGHT"

call :begin "Check required tools" "where docker kubectl helm curl go"
set "MISSING="
for %%T in (docker kubectl helm curl) do (
  where %%T >nul 2>&1 || set "MISSING=!MISSING! %%T"
)
if not "%SKIP_GO_TESTS%"=="1" (
  where go >nul 2>&1 || set "MISSING=!MISSING! go"
)
if defined MISSING (
  echo Missing from PATH:!MISSING!> "!STEPLOG!"
  call :end 1
) else (
  echo All required tools found.> "!STEPLOG!"
  call :end 0
)
if defined ABORT goto :summary

call :begin "Docker daemon is running" "docker info"
docker info >"!STEPLOG!" 2>&1
call :end
if defined ABORT goto :summary

call :begin "Kubernetes cluster is reachable" "kubectl config current-context / kubectl get nodes"
(kubectl config current-context & kubectl get nodes) >"!STEPLOG!" 2>&1
call :end
call :show
if defined ABORT goto :summary

call :begin "Policy engine token API answers on this PC" "curl -X POST %ENGINE_HOST_URL%/api/v1/dev/token?clientId=%CLIENT_ID%"
curl.exe -sS -f -X POST "%ENGINE_HOST_URL%/api/v1/dev/token?clientId=%CLIENT_ID%&ttlSeconds=%TTL_SECONDS%" >"!STEPLOG!" 2>&1
set "RC=!ERRORLEVEL!"
if "!RC!"=="0" (
  findstr /c:"access_token" "!STEPLOG!" >nul 2>&1 || (
    echo Response did not contain an access_token field:>>"!STEPLOG!"
    set "RC=1"
  )
) else (
  echo.>>"!STEPLOG!"
  echo Is the CRS policy engine running on %ENGINE_HOST_URL% ?>>"!STEPLOG!"
)
call :end !RC!
if defined ABORT goto :summary

rem ============================================================================
rem  PHASE 2 - BUILD
rem ============================================================================
call :phase "PHASE 2 - BUILD"

if "%SKIP_GO_TESTS%"=="1" goto :skip_go
call :begin "go vet ./..." "go vet ./..."
go vet ./... >"!STEPLOG!" 2>&1
call :end
if defined ABORT goto :summary

call :begin "go test ./..." "go test ./..."
go test ./... >"!STEPLOG!" 2>&1
call :end
if defined ABORT goto :summary
:skip_go

call :begin "Build Docker image %IMG%" "docker build -t %IMG% ."
docker build -t %IMG% . >"!STEPLOG!" 2>&1
call :end
if defined ABORT goto :summary

rem ============================================================================
rem  PHASE 3 - DEPLOY
rem ============================================================================
call :phase "PHASE 3 - DEPLOY"

call :begin "Fetch Helm chart dependencies (Kyverno)" "helm repo add / update / dependency update"
(
  helm repo add kyverno %KYVERNO_REPO%
  helm repo update
  helm dependency update deploy\helm\crsproxyagent\
) >"!STEPLOG!" 2>&1
call :end
if defined ABORT goto :summary

call :begin "Helm install operator + Kyverno + tenant policy" "helm upgrade --install %RELEASE% ... --wait"
set "INSTALL_STARTED=1"
helm upgrade --install %RELEASE% deploy\helm\crsproxyagent\ ^
  --namespace %NAMESPACE% --create-namespace ^
  --set clusterID=%CLUSTER_ID% ^
  --set operator.image.repository=%IMG_REPO% ^
  --set operator.image.tag=%IMG_TAG% ^
  --set operator.image.pullPolicy=%PULL_POLICY% ^
  --set operator.replicas=1 ^
  --set policyAPI.endpoint=%POLICY_API_URL% ^
  --set tokenAPI.endpoint=%TOKEN_API_URL% ^
  --set "policyAPI.tenants[0].id=%TENANT_ID%" ^
  --set "policyAPI.tenants[0].clientId=%CLIENT_ID%" ^
  --set "policyAPI.tenants[0].ttlSeconds=%TTL_SECONDS%" ^
  --set policyAPI.policy.reconcileInterval=%RECONCILE_INTERVAL% ^
  --set policyAPI.policy.tokenCache.enabled=%TOKEN_CACHE_ENABLED% ^
  %EXTRA_HELM_ARGS% ^
  --wait --timeout 600s >"!STEPLOG!" 2>&1
call :end
if defined ABORT goto :summary

call :begin "Operator and Kyverno pods are up" "kubectl -n %NAMESPACE% get pods"
kubectl -n %NAMESPACE% get pods >"!STEPLOG!" 2>&1
call :end
call :show
if defined ABORT goto :summary

call :begin "Kyverno CRD supports apiCall service headers (needs Kyverno 1.13+)" "kubectl explain clusterpolicy.spec.rules.context.apiCall.service"
kubectl explain clusterpolicy.spec.rules.context.apiCall.service >"!STEPLOG!" 2>&1
set "RC=!ERRORLEVEL!"
if "!RC!"=="0" (
  findstr /i /c:"headers" "!STEPLOG!" >nul 2>&1 || (
    echo The installed Kyverno CRD has no service.headers field, so the Authorization header would be dropped.>>"!STEPLOG!"
    echo Use a Kyverno chart 3.3.0 or newer ^(Kyverno 1.13+^), check deploy\helm\crsproxyagent\Chart.yaml.>>"!STEPLOG!"
    set "RC=1"
  )
)
call :end !RC!
if defined ABORT goto :summary

rem Locate the operator deployment (used for log collection).
for /f "delims=" %%D in ('kubectl -n %NAMESPACE% get deploy -o name 2^>nul ^| findstr /i "crsproxyagent" ^| findstr /v /i "kyverno"') do (
  if not defined OPERATOR_FOUND (
    set "OPERATOR_DEPLOY=%%D"
    set "OPERATOR_FOUND=1"
  )
)

if /i not "%TOKEN_CACHE_ENABLED%"=="true" goto :skip_cache_check
call :begin "Operator created token cache entry crs-token-%TENANT_ID%" "kubectl get globalcontextentry crs-token-%TENANT_ID% (polls up to %WAIT_POLICY_SECS%s)"
set /a WAITED=0
:wait_cache
kubectl get globalcontextentry crs-token-%TENANT_ID% >"!STEPLOG!" 2>&1
if not errorlevel 1 goto :cache_found
set /a WAITED+=5
if !WAITED! geq %WAIT_POLICY_SECS% goto :cache_timeout
ping -n 6 127.0.0.1 >nul
goto :wait_cache
:cache_timeout
echo.>>"!STEPLOG!"
echo Token cache entry was not created within %WAIT_POLICY_SECS%s. Operator log:>>"!STEPLOG!"
kubectl -n %NAMESPACE% logs %OPERATOR_DEPLOY% --tail=40 >>"!STEPLOG!" 2>&1
call :end 1
if defined ABORT goto :summary
:cache_found
call :end 0
call :show
:skip_cache_check

call :begin "Operator created ClusterPolicy %POLICY_NAME%" "kubectl get clusterpolicy %POLICY_NAME% (polls up to %WAIT_POLICY_SECS%s)"
set /a WAITED=0
:wait_policy
kubectl get clusterpolicy %POLICY_NAME% >"!STEPLOG!" 2>&1
if not errorlevel 1 goto :policy_found
set /a WAITED+=5
if !WAITED! geq %WAIT_POLICY_SECS% goto :policy_timeout
ping -n 6 127.0.0.1 >nul
goto :wait_policy
:policy_timeout
echo.>>"!STEPLOG!"
echo ClusterPolicy %POLICY_NAME% was not created within %WAIT_POLICY_SECS%s. Operator log:>>"!STEPLOG!"
kubectl -n %NAMESPACE% logs %OPERATOR_DEPLOY% --tail=40 >>"!STEPLOG!" 2>&1
call :end 1
if defined ABORT goto :summary
:policy_found
call :end 0
call :show

call :begin "ClusterPolicy is Ready in Kyverno" "kubectl wait --for=condition=Ready clusterpolicy/%POLICY_NAME%"
kubectl wait --for=condition=Ready clusterpolicy/%POLICY_NAME% --timeout=120s >"!STEPLOG!" 2>&1
call :end
if defined ABORT goto :summary

call :begin "Generated policy: token call first, then policy call with Bearer token" "kubectl get clusterpolicy %POLICY_NAME% -o yaml"
kubectl get clusterpolicy %POLICY_NAME% -o yaml >"%POLICY_DUMP%" 2>"!STEPLOG!"
set "RC=!ERRORLEVEL!"
if "!RC!"=="0" (
  set "MISSING="
  findstr /c:"name: crsToken" "%POLICY_DUMP%" >nul 2>&1            || set "MISSING=!MISSING! [token context crsToken]"
  findstr /c:"name: crsDecision" "%POLICY_DUMP%" >nul 2>&1         || set "MISSING=!MISSING! [decision context crsDecision]"
  if /i "%TOKEN_CACHE_ENABLED%"=="true" (
    findstr /c:"globalReference" "%POLICY_DUMP%" >nul 2>&1         || set "MISSING=!MISSING! [globalReference to token cache]"
    findstr /c:"crs-token-%TENANT_ID%" "%POLICY_DUMP%" >nul 2>&1   || set "MISSING=!MISSING! [token cache entry name]"
  ) else (
    findstr /c:"/api/v1/dev/token" "%POLICY_DUMP%" >nul 2>&1       || set "MISSING=!MISSING! [token API url]"
    findstr /c:"clientId=%CLIENT_ID%" "%POLICY_DUMP%" >nul 2>&1    || set "MISSING=!MISSING! [clientId in token url]"
  )
  findstr /c:"/api/v1/admission/evaluate" "%POLICY_DUMP%" >nul 2>&1 || set "MISSING=!MISSING! [policy API url]"
  findstr /c:"Bearer {{ crsToken }}" "%POLICY_DUMP%" >nul 2>&1     || set "MISSING=!MISSING! [Authorization header uses token]"
  if defined MISSING (
    echo Generated ClusterPolicy is missing:!MISSING!> "!STEPLOG!"
    echo See %POLICY_DUMP%>> "!STEPLOG!"
    set "RC=1"
  ) else (
    echo Policy dumped to %POLICY_DUMP%> "!STEPLOG!"
  )
)
call :end !RC!
if defined ABORT goto :summary

call :begin "Kyverno admission controller is ready" "kubectl rollout status deployment (admission controller)"
set "ADM_DEPLOY="
for /f "delims=" %%A in ('kubectl -n %NAMESPACE% get deploy -o name 2^>nul ^| findstr /i "admission"') do (
  if not defined ADM_DEPLOY set "ADM_DEPLOY=%%A"
)
if not defined ADM_DEPLOY (
  echo Could not find the Kyverno admission controller deployment.> "!STEPLOG!"
  call :end 1
) else (
  kubectl -n %NAMESPACE% rollout status !ADM_DEPLOY! --timeout=120s >"!STEPLOG!" 2>&1
  call :end
)
if defined ABORT goto :summary

rem ============================================================================
rem  PHASE 4 - END-TO-END ADMISSION TESTS (failures here do not stop the run)
rem ============================================================================
call :phase "PHASE 4 - END-TO-END ADMISSION TESTS"
set "FATAL=0"

call :begin "Create test namespace %TEST_NS% and sample pod manifests" "kubectl create namespace %TEST_NS%"
(
  kubectl get namespace %TEST_NS% >nul 2>&1 || kubectl create namespace %TEST_NS%
) >"!STEPLOG!" 2>&1
set "RC=!ERRORLEVEL!"
call :end !RC!

rem Give Kyverno a moment to register the admission webhook for the new policy.
echo     waiting 10s for Kyverno webhooks to settle...
ping -n 11 127.0.0.1 >nul

call :begin "TEST 1: compliant pod is ADMITTED" "kubectl apply --dry-run=server -f pod-allow.yaml  (expect: created)"
set /a TRY=1
:test1_try
kubectl apply --dry-run=server -f "%ALLOW_YAML%" >"!STEPLOG!" 2>&1
if not errorlevel 1 goto :test1_done
if !TRY! geq 3 goto :test1_done
findstr /i /c:"context deadline exceeded" /c:"connection refused" /c:"no endpoints" "!STEPLOG!" >nul 2>&1
if errorlevel 1 goto :test1_done
echo       transient webhook error ^(attempt !TRY! of 3^), retrying in 10s...
set /a TRY+=1
ping -n 11 127.0.0.1 >nul
goto :test1_try
:test1_done
call :end
call :show

call :begin "TEST 2: non-compliant pod is DENIED by the policy engine" "kubectl apply --dry-run=server -f pod-deny.yaml  (expect: denied)"
kubectl apply --dry-run=server -f "%DENY_YAML%" >"!STEPLOG!" 2>&1
set "RC=!ERRORLEVEL!"
if "!RC!"=="0" (
  echo The pod was ADMITTED but the policy engine should have denied it.>>"!STEPLOG!"
  echo Check the engine rules for image %DENY_IMAGE% / gpuCount %DENY_GPU_COUNT%.>>"!STEPLOG!"
  call :end 1
) else (
  findstr /i /c:"denied" "!STEPLOG!" >nul 2>&1
  if errorlevel 1 (
    echo kubectl failed, but not with a policy denial ^(webhook/apiCall problem?^):>>"!STEPLOG!"
    call :end 1
  ) else (
    call :end 0
    call :show
  )
)

call :begin "TEST 3: pod outside %TEST_NS% is NOT affected by the policy" "kubectl run --dry-run=server in namespace default"
kubectl -n default run crs-scope-test --image=%DENY_IMAGE% --dry-run=server -o name >"!STEPLOG!" 2>&1
call :end

rem ============================================================================
rem  OPTIONAL CLEANUP
rem ============================================================================
if not "%CLEANUP%"=="1" goto :summary
call :phase "CLEANUP"
call :begin "Uninstall release and delete test namespace" "helm uninstall %RELEASE% / kubectl delete namespace %TEST_NS%"
(
  helm uninstall %RELEASE% --namespace %NAMESPACE%
  kubectl delete namespace %TEST_NS% --ignore-not-found
) >"!STEPLOG!" 2>&1
call :end

rem ============================================================================
rem  SUMMARY
rem ============================================================================
:summary
set "FATAL=0"
if %FAILS% gtr 0 if "%INSTALL_STARTED%"=="1" call :diagnostics

echo.
echo !CYAN!=== SUMMARY ===!RESET!
for /l %%i in (1,1,!N!) do (
  set "R=!RES_%%i!"
  set "T=!NAME_%%i!"
  if "!R!"=="PASS" (
    echo   !GREEN![PASS]!RESET! %%i. !T!
  ) else (
    echo   !RED![FAIL]!RESET! %%i. !T!
  )
)
echo.
if %FAILS% equ 0 (
  echo !GREEN!RESULT: SUCCESS - all %N% steps passed.!RESET!
  echo Logs: %LOGDIR%
  endlocal & exit /b 0
) else (
  echo !RED!RESULT: FAILURE - %FAILS% of %N% steps failed.!RESET!
  echo Per-step logs: %LOGDIR%
  endlocal & exit /b 1
)

rem ============================================================================
rem  SUBROUTINES
rem ============================================================================
:phase
echo.
echo !CYAN!--- %~1 ---!RESET!
goto :eof

:begin
set /a N+=1
set "NN=0!N!"
set "STEPLOG=%LOGDIR%\step-!NN:~-2!.log"
set "NAME_!N!=%~1"
set "RES_!N!=FAIL"
echo.
echo !YELLOW![!N!]!RESET! %~1
if not "%~2"=="" echo       $ %~2
goto :eof

:end
set "RC=%ERRORLEVEL%"
if not "%~1"=="" set "RC=%~1"
if "!RC!"=="0" (
  set "RES_!N!=PASS"
  echo       !GREEN!PASS!RESET!
) else (
  set "RES_!N!=FAIL"
  set /a FAILS+=1
  echo       !RED!FAIL!RESET! ^(exit code !RC!^)
  echo       --- error output ^(last 25 lines of !STEPLOG!^) ---
  powershell -NoProfile -Command "Get-Content -LiteralPath '!STEPLOG!' -Tail 25 -ErrorAction SilentlyContinue | ForEach-Object { '        ' + $_ }"
  if "!FATAL!"=="1" (
    set "ABORT=1"
    echo       !RED!Stopping: this step is required for the steps that follow.!RESET!
  )
)
goto :eof

:show
if not exist "!STEPLOG!" goto :eof
powershell -NoProfile -Command "Get-Content -LiteralPath '!STEPLOG!' -Tail 40 -ErrorAction SilentlyContinue | ForEach-Object { '        ' + $_ }"
goto :eof

:diagnostics
echo.
echo !YELLOW!--- DIAGNOSTICS: pod state and recent operator / Kyverno logs ---!RESET!
set "DIAG=%LOGDIR%\diagnostics.log"
(
  echo ===== pods =====
  kubectl -n %NAMESPACE% get pods
  echo.
  echo ===== operator log =====
  kubectl -n %NAMESPACE% logs %OPERATOR_DEPLOY% --tail=60
  echo.
  echo ===== kyverno admission controller log =====
  for /f "delims=" %%A in ('kubectl -n %NAMESPACE% get deploy -o name 2^>nul ^| findstr /i "admission"') do kubectl -n %NAMESPACE% logs %%A --tail=60
) >"%DIAG%" 2>&1
powershell -NoProfile -Command "Get-Content -LiteralPath '%DIAG%' -Tail 60 | ForEach-Object { '  ' + $_ }"
echo   Full diagnostics: %DIAG%
goto :eof
