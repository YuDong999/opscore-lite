// DynamicForm: 通用表单渲染, 接受 schema 数组, 渲染 string/number/bool/select/code/secret
// 提交时回 values 字典; 不做异步提交, 由调用方处理。
// 简易实现: 必填校验在 onSubmit 时由调用方处理, 这里只保证渲染。

import { useState } from 'react'

export type FormValue = string | number | boolean | undefined
export type FormValues = Record<string, FormValue>

export interface FormField {
  name: string
  label: string
  type: 'string' | 'number' | 'bool' | 'select' | 'code' | 'secret'
  required?: boolean
  help?: string
  options?: { label: string; value: string }[]
  min?: number
  max?: number
  pattern?: string
}

export interface DynamicFormProps {
  fields: FormField[]
  values: FormValues
  onChange: (v: FormValues) => void
  disabled?: boolean
}

export default function DynamicForm({ fields, values, onChange, disabled }: DynamicFormProps) {
  if (!fields.length) return null
  return (
    <div className="dyn-form">
      {fields.map((f) => (
        <div className="dyn-form-row" key={f.name}>
          <label className="dyn-form-label">
            {f.label}
            {f.required && <span className="dyn-form-req">*</span>}
          </label>
          <div className="dyn-form-input">
            <FieldRender field={f} value={values[f.name]}
              onChange={(v) => onChange({ ...values, [f.name]: v })}
              disabled={disabled} />
            {f.help && <div className="dyn-form-help">{f.help}</div>}
          </div>
        </div>
      ))}
    </div>
  )
}

function FieldRender({
  field, value, onChange, disabled,
}: {
  field: FormField
  value: FormValue
  onChange: (v: FormValue) => void
  disabled?: boolean
}) {
  const common = { disabled }
  switch (field.type) {
    case 'string':
      return <input className="input" type="text" value={(value as string) ?? ''} {...common}
        onChange={(e) => onChange(e.target.value)} />
    case 'secret':
      return <input className="input" type="password" value={(value as string) ?? ''} {...common}
        onChange={(e) => onChange(e.target.value)} />
    case 'number': {
      const v = value as number | undefined
      return <input className="input" type="number" value={v ?? ''} {...common}
        min={field.min} max={field.max}
        onChange={(e) => onChange(e.target.value === '' ? undefined : Number(e.target.value))} />
    }
    case 'bool':
      return <label className="dyn-form-bool">
        <input type="checkbox" checked={!!value} {...common}
          onChange={(e) => onChange(e.target.checked)} />
        <span>{value ? '是' : '否'}</span>
      </label>
    case 'select':
      return (
        <select className="input" value={(value as string) ?? ''} {...common}
          onChange={(e) => onChange(e.target.value)}>
          <option value="" disabled>请选择</option>
          {(field.options || []).map((o) => (
            <option key={o.value} value={o.value}>{o.label}</option>
          ))}
        </select>
      )
    case 'code':
      return <CodeEditor value={(value as string) ?? ''} {...common} onChange={onChange} />
    default:
      return <input className="input" type="text" value={String(value ?? '')} {...common}
        onChange={(e) => onChange(e.target.value)} />
  }
}

// CodeEditor: 多行 + 等宽字体, 简单 textarea, 保持 8 行
function CodeEditor({
  value, onChange, disabled,
}: { value: string; onChange: (v: string) => void; disabled?: boolean }) {
  return (
    <textarea className="input dyn-form-code" rows={8} spellCheck={false}
      value={value} disabled={disabled}
      onChange={(e) => onChange(e.target.value)} />
  )
}
