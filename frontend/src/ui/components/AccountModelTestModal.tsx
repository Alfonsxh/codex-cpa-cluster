import { Alert, Button, Modal, Select, Spin } from "antd";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { readAccountModels, testAccountModel, type Account } from "../../api/accounts";
import { formatSiteTimestamp } from "../site-time";

export function AccountModelTestModal({ account, csrfToken, onClose }: {
  account: Account;
  csrfToken: string;
  onClose: () => void;
}) {
  const [model, setModel] = useState("");
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);
  const models = useQuery({
    queryKey: ["account-models", account.id],
    queryFn: ({ signal }) => readAccountModels(account.id, signal),
    retry: false, gcTime: 0, staleTime: 0, refetchOnWindowFocus: false
  });
  const options = models.data?.models ?? [];
  const selected = options.includes(model) ? model : options.includes("gpt-6-astra") ? "gpt-6-astra" : options[0] ?? "";
  const test = useMutation({
    mutationFn: async () => {
      controller.current = new AbortController();
      return testAccountModel(account.id, selected, csrfToken, controller.current.signal);
    },
    retry: false
  });
  const result = test.data;
  return <Modal open title="模型通信测试" className="account-model-test-modal" width={560}
    onCancel={onClose} footer={[
      <Button key="close" onClick={onClose}>{test.isPending ? "取消测试" : "关闭"}</Button>,
      <Button key="test" type="primary" loading={test.isPending} disabled={!selected || models.isFetching || models.isError}
        onClick={() => { test.reset(); test.mutate(); }}>{result ? "重新测试" : "开始测试"}</Button>
    ]}>
    <div className="account-model-test-content">
      <div className="account-model-test-account"><strong>{account.id}</strong><span>{account.email}</span></div>
      <label className="account-model-test-select"><span>测试模型</span>
        <Select aria-label="测试模型" showSearch value={selected || undefined} loading={models.isFetching}
          disabled={test.isPending || models.isFetching || models.isError} options={options.map(value => ({ value, label: value }))}
          placeholder="请选择模型" onChange={value => { setModel(value); test.reset(); }} />
      </label>
      <p className="field-help">向此账号发送一条简短生成请求，验证实际模型通信。最长等待 25 秒，会产生少量用量。</p>
      {models.isError ? <Alert type="error" showIcon title="模型列表读取失败" description={models.error.message}
        action={<Button size="small" onClick={() => void models.refetch()}>重试</Button>} /> : null}
      {!models.isPending && !models.isError && options.length === 0 ? <Alert type="warning" showIcon title="此账号尚未提供可测试的模型" /> : null}
      {test.isPending ? <div className="account-model-test-pending" role="status"><Spin size="small" />正在测试 {selected}…</div> : null}
      {test.isError ? <Alert type="error" showIcon title="测试未完成" description={test.error.message} /> : null}
      {result && !test.isPending ? <div role="status" className="account-model-test-result">
        <Alert type={result.success ? "success" : "error"} showIcon title={result.success ? "模型通信正常" : "模型通信失败"} description={result.message} />
        <dl><div><dt>模型</dt><dd>{result.model}</dd></div>
          <div><dt>耗时</dt><dd>{(result.elapsed_ms / 1000).toFixed(2)} 秒</dd></div>
          <div><dt>HTTP 状态</dt><dd>{result.upstream_status || "未收到响应"}</dd></div>
          <div><dt>测试时间</dt><dd>{formatSiteTimestamp(result.checked_at)}</dd></div></dl>
      </div> : null}
    </div>
  </Modal>;
}
