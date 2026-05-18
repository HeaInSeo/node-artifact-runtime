# NAN Input Materialization Draft

문서 상태: Draft v0  
작성일: 2026-05-18

## 목적

이 문서는 `nan`이 향후 입력 acquisition/materialization을 맡게 될 경우의 확장 지점을 정리한다.

현재 `nan` v0의 공식 범위는 output manifest 생성이다.

즉 이 문서는 구현 지시서가 아니라, v1 이후 확장을 위한 경계 문서다.

## 현재 원칙

v0에서 `nan`은 아래만 담당한다.

- user command argv 실행
- output inspect
- digest/size 계산
- manifest 작성

v0에서 `nan`은 아래를 담당하지 않는다.

- `MaterializationPlan` 실행
- remote fetch
- local reuse 판단
- input digest verify before exec

## 확장 입력

향후 JUMI가 AH `ResolveHandoff` 결과를 node contract에 기록하면, `nan`은 다음 입력을 받을 수 있다.

```json
{
  "inputs": [
    {
      "bindingName": "bam",
      "mountPath": "/in/result.bam",
      "materializationPlan": {
        "mode": "remote_fetch",
        "uri": "https://artifact.example/result.bam",
        "expectedDigest": "sha256:..."
      }
    }
  ]
}
```

## 예상 책임

`nan`이 input materialization을 맡게 되면 최소한 아래가 필요하다.

- mode별 분기
- `/in` 아래 경로 배치
- digest verify
- partial download cleanup
- pre-exec failure summary

## mode 후보

- `none`
- `local_reuse`
- `remote_fetch`
- `unavailable`

이 mode의 최종 의미는 JUMI/AH와 함께 고정되어야 한다.

## 아직 미결정인 것

- download transport 구현 위치
- retry 정책을 nan이 가질지 여부
- input cache 정책
- local_reuse에서 실제 node-local path를 누가 제공하는지
- digest mismatch 시 exit code 정책

## 결론

현재 `nan` 저장소에서는 input materialization을 구현하지 않는다.

다만 이후 JUMI/AH 전환에서 범위가 커질 것을 대비해, `MaterializationPlan` 실행은
별도 단계로 다룬다.
