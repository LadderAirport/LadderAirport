import { Checkbox } from './ui/checkbox'
import { Input } from './ui/input'
import { Label } from './ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from './ui/select'
import type { Field } from '../api/client'
import { cn } from '@/lib/utils'
import { Dices } from 'lucide-react'

/** Radix Select rejects empty-string item values; use a sentinel for the "(默认)" option. */
const SELECT_EMPTY_VALUE = '__default__'

export interface DynamicFormProps {
  fields: Field[]
  value: Record<string, unknown>
  onChange: (next: Record<string, unknown>) => void
  errors?: Record<string, string>
  disabled?: boolean
}

export default function DynamicForm({
  fields,
  value,
  onChange,
  errors = {},
  disabled = false,
}: DynamicFormProps) {
  function setField(name: string, v: unknown) {
    onChange({ ...value, [name]: v })
  }

  return (
    <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
      {fields.map((f) => {
        const current =
          value[f.name] !== undefined && value[f.name] !== null
            ? value[f.name]
            : f.default !== undefined
              ? f.default
              : f.type === 'bool'
                ? false
                : ''
        const error = errors[f.name]

        if (f.type === 'bool') {
          return (
            <div className="flex flex-col space-y-2 border border-border/50 rounded-lg p-3 bg-muted/10" key={f.name}>
              <div className="flex items-center space-x-2">
                <Checkbox
                  id={`field-${f.name}`}
                  name={f.name}
                  checked={Boolean(current)}
                  disabled={disabled}
                  onCheckedChange={(checked) => setField(f.name, checked)}
                />
                <Label htmlFor={`field-${f.name}`} className="text-sm font-medium leading-none cursor-pointer">
                  {f.label}
                  {f.required ? <span className="text-red-500"> *</span> : null}
                </Label>
              </div>
              {f.description ? <p className="text-xs text-muted-foreground pl-6">{f.description}</p> : null}
              {error ? (
                <p className="text-xs text-red-500 font-medium pl-6" role="alert">
                  {error}
                </p>
              ) : null}
            </div>
          )
        }

        return (
          <div className="flex flex-col space-y-2" key={f.name}>
            <Label htmlFor={`field-${f.name}`} className="text-sm font-medium text-foreground">
              {f.label}
              {f.required ? <span className="text-red-500"> *</span> : null}
            </Label>
            {f.description ? <p className="text-xs text-muted-foreground">{f.description}</p> : null}
            <div className="relative">
              {renderInput(f, current, setField, error, disabled)}
            </div>
            {error ? (
              <p className="text-xs text-red-500 font-medium" role="alert">
                {error}
              </p>
            ) : null}
          </div>
        )
      })}
    </div>
  )
}

function renderInput(
  f: Field,
  current: unknown,
  setField: (name: string, v: unknown) => void,
  error?: string,
  disabled = false,
) {
  const isPort = f.name === 'port' || f.name.endsWith('_port')
  const inputId = `field-${f.name}`

  switch (f.type) {
    case 'int':
      return (
        <div className="relative flex items-center w-full">
          <Input
            id={inputId}
            name={f.name}
            type="number"
            min={isPort ? 1 : 0}
            max={isPort ? 65535 : undefined}
            disabled={disabled}
            className={cn(
              error ? 'border-red-500 focus-visible:ring-red-500' : '',
              isPort ? 'pr-10' : ''
            )}
            value={current === '' || current == null ? '' : Number(current)}
            onChange={(e) => {
              const v = e.target.value
              setField(f.name, v === '' ? '' : Number(v))
            }}
            placeholder={f.default != null && f.default !== '' ? `默认 ${String(f.default)}` : undefined}
          />
          {isPort && (
            <button
              type="button"
              disabled={disabled}
              onClick={() => {
                // Generate a random high port: 10000 - 65535
                const randPort = Math.floor(Math.random() * (65535 - 10000 + 1)) + 10000
                setField(f.name, randPort)
              }}
              className="absolute right-1 p-1.5 text-muted-foreground hover:text-foreground bg-muted/60 hover:bg-muted rounded transition-colors disabled:opacity-50 disabled:pointer-events-none cursor-pointer h-7 w-7 flex items-center justify-center select-none"
              title="随机生成高端口"
            >
              <Dices className="h-4 w-4" />
            </button>
          )}
        </div>
      )
    case 'select':
      const selectVal = current == null || current === '' ? SELECT_EMPTY_VALUE : String(current)
      return (
        <Select
          disabled={disabled}
          value={selectVal}
          onValueChange={(val) => setField(f.name, val === SELECT_EMPTY_VALUE ? '' : val)}
        >
          <SelectTrigger className={error ? 'border-red-500 focus-visible:ring-red-500' : ''} id={inputId}>
            <SelectValue placeholder="请选择" />
          </SelectTrigger>
          <SelectContent>
            {(f.options ?? []).map((opt) => (
              <SelectItem key={opt || SELECT_EMPTY_VALUE} value={opt === '' ? SELECT_EMPTY_VALUE : opt}>
                {opt === '' ? '(默认 / 不限制)' : opt}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )
    case 'password':
      return (
        <Input
          id={inputId}
          name={f.name}
          type="password"
          autoComplete="new-password"
          disabled={disabled}
          className={error ? 'border-red-500 focus-visible:ring-red-500' : ''}
          value={current == null ? '' : String(current)}
          onChange={(e) => setField(f.name, e.target.value)}
          placeholder="留空则服务端自动生成"
        />
      )
    case 'string':
    default:
      return (
        <Input
          id={inputId}
          name={f.name}
          disabled={disabled}
          className={error ? 'border-red-500 focus-visible:ring-red-500' : ''}
          value={current == null ? '' : String(current)}
          onChange={(e) => setField(f.name, e.target.value)}
          placeholder={
            f.default != null && f.default !== ''
              ? `默认 ${String(f.default)}`
              : undefined
          }
        />
      )
  }
}

/** Build initial params object from template field defaults. */
export function defaultsFromFields(fields: Field[]): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const f of fields) {
    if (f.default !== undefined) {
      out[f.name] = f.default
    } else if (f.type === 'bool') {
      out[f.name] = false
    } else if (f.type === 'int') {
      out[f.name] = ''
    } else {
      out[f.name] = ''
    }
  }
  return out
}

/** Extract only fields exposed by a template, preserving defaults for missing values. */
export function valuesFromFields(
  fields: Field[],
  source: Record<string, unknown> | null | undefined,
): Record<string, unknown> {
  const out = defaultsFromFields(fields)
  if (!source) return out
  for (const field of fields) {
    if (Object.prototype.hasOwnProperty.call(source, field.name)) {
      out[field.name] = source[field.name]
    }
  }
  return out
}

/** Validate template-visible fields before sending them to the API. */
export function validateDynamicFields(
  fields: Field[],
  value: Record<string, unknown>,
): Record<string, string> {
  const errors: Record<string, string> = {}
  for (const field of fields) {
    const current = value[field.name]
    const empty =
      current === undefined ||
      current === null ||
      (typeof current === 'string' && current.trim() === '')
    if (field.required && empty) {
      errors[field.name] = `请填写${field.label}`
      continue
    }
    if (empty) continue

    if (field.type === 'int') {
      const n = Number(current)
      if (!Number.isFinite(n) || !Number.isInteger(n)) {
        errors[field.name] = `${field.label}必须是整数`
        continue
      }
      const isPort = field.name === 'port' || field.name.endsWith('_port')
      if (isPort && (n < 1 || n > 65535)) {
        errors[field.name] = `${field.label}必须在 1 到 65535 之间`
      } else if (!isPort && n < 0) {
        errors[field.name] = `${field.label}不能小于 0`
      }
    }

    if (
      field.type === 'select' &&
      field.options &&
      !field.options.includes(String(current))
    ) {
      errors[field.name] = `请选择有效的${field.label}`
    }
  }
  return errors
}
