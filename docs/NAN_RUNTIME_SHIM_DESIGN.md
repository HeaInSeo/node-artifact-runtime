# NAN Runtime Shim 설계 문서

문서 상태: Draft v0.1  
작성일: 2026-05-17  
대상 프로젝트: `node-artifact-runtime` / `JUMI` / `artifact-handoff(AH)`  
핵심 결정: `nan`은 sidecar나 background daemon이 아니라, DAG node 컨테이너의 main entrypoint로 실행되는 runtime shim이다.

## 1. 목적

이 문서는 JUMI, AH, node-artifact-runtime 세 프로젝트가 Kubernetes 데이터 플레인에서 함께 동작할 때, `node-artifact-runtime`, 이하 `nan`, 이 어떤 책임을 가져야 하는지 확정하기 위한 개발 설계 문서다.

최종 결정은 다음과 같다.

```text
JUMI
  - DAG 실행 주체
  - attemptID 관리
  - Pod/Job 생성 및 관찰
  - nan manifest 회수
  - AH RegisterArtifact 호출

nan
  - node 컨테이너 내부 runtime shim
  - 사용자 명령을 argv 그대로 실행
  - 사용자 명령 종료 후 output 검사
  - digest/size/uri manifest 작성
  - AH 직접 호출하지 않음

AH
  - artifact registry/resolver
  - RegisterArtifact 저장
  - ResolveHandoff 결정
  - location/GC/policy 판단
```

즉 nan은 JUMI의 executor를 대체하지 않는다. nan은 DAG를 모른다. nan은 AH에 직접 artifact를 등록하지 않는다. nan은 Kubernetes Pod/Job을 만들지 않는다. nan은 오직 컨테이너 내부 실행 계약과 산출물 manifest 생성을 담당한다.

## 2. runtime shim 결정

권장 실행 모델:

```text
컨테이너 시작
  ↓
nan run --contract /jumi/node-contract.json -- <user command argv...>
  ↓
nan이 user command를 child process로 실행
  ↓
user command 종료 대기
  ↓
exitCode != 0 이면 nan도 동일 exitCode로 종료
  ↓
exitCode == 0 이면 /out 검사
  ↓
manifest atomic write
  ↓
nan exit 0
  ↓
컨테이너 종료
  ↓
JUMI가 Pod/Job 종료 감지
  ↓
JUMI가 manifest 회수
  ↓
JUMI가 AH RegisterArtifact 호출
```

`nan`은 사용자의 명령을 대신 해석하지 않는다. JUMI가 정한 argv를 그대로 실행한다.

## 3. 책임 경계

nan이 해야 하는 것:

```text
- contract load
- user command argv 그대로 실행
- stdout/stderr passthrough
- signal forwarding
- child exit code 보존
- 성공 시 output inspect
- digest/size 계산
- manifest atomic write
```

nan이 하면 안 되는 것:

```text
- DAG dependency 판단
- node retry 결정
- AH RegisterArtifact 직접 호출
- AH ResolveHandoff 직접 호출
- Kubernetes Pod/Job 생성
- placement 결정
```

JUMI가 해야 하는 것:

```text
- attemptID 생성
- node-contract.json 생성
- nan command/args 주입
- AH ResolveHandoff 호출
- Pod/Job 생성
- Pod/Job 종료 관찰
- manifest 회수
- RegisterArtifact 호출
- node/run 상태 전이 기록
```

AH가 해야 하는 것:

```text
- artifact canonical key 검증
- immutability 보장
- ResolveHandoff 판단
- PlacementIntent / MaterializationPlan 제공
- GC / retention 정책 제공
```

## 4. contract file

권장 경로:

```text
/jumi/node-contract.json
```

예시:

```json
{
  "schemaVersion": "nan.nodeContract.v1",
  "runId": "run-001",
  "sampleRunId": "sample-001",
  "nodeId": "bwa",
  "attemptId": "run-001-bwa-attempt-1",
  "containerName": "main",
  "paths": {
    "inputRoot": "/in",
    "workRoot": "/work",
    "outputRoot": "/out",
    "manifestPath": "/out/_meta/jumi/runs/run-001/nodes/bwa/attempts/run-001-bwa-attempt-1/artifacts.manifest.json"
  },
  "outputs": [
    {
      "name": "bam",
      "path": "result.bam",
      "required": true,
      "type": "file"
    }
  ],
  "runtime": {
    "inspectOnSuccessOnly": true,
    "failOnMissingRequiredOutput": true,
    "allowDirectoryOutput": false
  }
}
```

우선순위:

```text
contract file > explicit flags > env defaults
```

## 5. manifest schema

권장 v1 schema:

```json
{
  "schemaVersion": "nan.artifactManifest.v1",
  "runId": "run-001",
  "sampleRunId": "sample-001",
  "nodeId": "bwa",
  "attemptId": "run-001-bwa-attempt-1",
  "containerName": "main",
  "nanVersion": "v0.1.0",
  "createdAt": "2026-05-17T00:00:00Z",
  "outputRoot": "/out",
  "artifacts": [
    {
      "outputName": "bam",
      "declaredPath": "result.bam",
      "absolutePath": "/out/result.bam",
      "uri": "jumi://runs/run-001/nodes/bwa/attempts/run-001-bwa-attempt-1/outputs/bam",
      "digest": "sha256:...",
      "sizeBytes": 12345,
      "type": "file"
    }
  ]
}
```

중요한 점:

```text
- attemptId 포함
- schemaVersion 포함
- createdAt 포함
- outputName과 relative path 분리
```

## 6. v0 범위

v0에서는 output manifest 생성에 집중한다.

```text
- main entrypoint shim
- child exit code 보존
- declared output 검증
- digest/size 계산
- manifest 작성
```

아직 v0 범위 밖:

```text
- MaterializationPlan 실행
- input acquisition / remote fetch
- AH 직접 호출
- sidecar / daemon model
```

`MaterializationPlan`과 입력 materialization은 v1 이후 별도 설계로 확장한다.

## 7. 테스트 전략

unit tests:

```text
- success -> manifest 생성
- child failure -> exit code 보존
- required output missing -> non-zero
- output path escape -> 실패
- absolute output path -> 실패
- directory output -> 실패
- atomic write 검증
- contract file parse 검증
```

integration tests:

```text
- A -> B -> C fixture
- JUMI가 manifest 회수
- JUMI가 AH RegisterArtifact 호출
- ResolveHandoff 후 child binding env 주입
```

## 8. 결론

최종 선택은 `nan main entrypoint runtime shim`이다.

이유:

```text
- sidecar lifecycle race를 피함
- background supervisor 구조보다 단순함
- Kubernetes Job 모델과 잘 맞음
- JUMI가 DAG/attempt/AH 등록 주체로 남음
- nan은 컨테이너 내부 산출물 계약에 집중함
```

가장 중요한 원칙:

> nan은 executor가 아니다. nan은 컨테이너 내부 runtime shim이다.  
> JUMI가 executor이고, AH가 handoff resolver이며, nan은 artifact manifest를 생성하는 trusted runtime boundary다.
