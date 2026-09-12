/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { AlertTriangle, GripVertical, Plus, Trash2 } from 'lucide-react'
import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  memo,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { safeJsonParse } from '../utils/json-parser'
import { GroupSpecialUsableRulesEditor } from './group-special-usable-editor'

type GroupRatioVisualEditorProps = {
  accountGroups: string
  defaultUserGroup: string
  groupRatio: string
  topupGroupRatio: string
  userUsableGroups: string
  groupGroupRatio: string
  autoGroups: string
  maxTokenAutoGroupsField: ReactNode
  groupSpecialUsableGroup: string
  defaultUseAutoGroup: boolean
  onChange: (field: string, value: string) => void
}

export type AccountGroupRow = {
  _id: string
  name: string
  description: string
  topupRatio: string
}

export type RouteGroupRow = {
  _id: string
  name: string
  description: string
  ratio: string
  selectable: boolean
}

type OverrideRow = {
  _id: string
  accountGroup: string
  routeGroup: string
  ratio: string
}

const sectionCardClassName =
  'relative shadow-sm ring-0 before:pointer-events-none before:absolute before:inset-0 before:rounded-xl before:border before:border-border/90'
const sectionHeaderClassName = 'border-b bg-muted/20'

let rowIdCounter = 0
function createRowId(prefix: string) {
  rowIdCounter += 1
  return `${prefix}_${rowIdCounter}`
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function parseRatioMap(value: string): Record<string, number> {
  const parsed = safeJsonParse<unknown>(value, {
    fallback: {},
    silent: true,
  })
  if (!isRecord(parsed)) return {}

  const result: Record<string, number> = {}
  for (const [name, rawRatio] of Object.entries(parsed)) {
    const ratio = Number(rawRatio)
    if (Number.isFinite(ratio)) result[name] = ratio
  }
  return result
}

function parseStringMap(value: string): Record<string, string> {
  const parsed = safeJsonParse<unknown>(value, {
    fallback: {},
    silent: true,
  })
  if (!isRecord(parsed)) return {}

  const result: Record<string, string> = {}
  for (const [name, description] of Object.entries(parsed)) {
    if (typeof description === 'string') result[name] = description
  }
  return result
}

function parseNestedRatioMap(
  value: string
): Record<string, Record<string, number>> {
  const parsed = safeJsonParse<unknown>(value, {
    fallback: {},
    silent: true,
  })
  if (!isRecord(parsed)) return {}

  const result: Record<string, Record<string, number>> = {}
  for (const [accountGroup, rawRoutes] of Object.entries(parsed)) {
    if (!isRecord(rawRoutes)) continue
    const routes: Record<string, number> = {}
    for (const [routeGroup, rawRatio] of Object.entries(rawRoutes)) {
      const ratio = Number(rawRatio)
      if (Number.isFinite(ratio)) routes[routeGroup] = ratio
    }
    result[accountGroup] = routes
  }
  return result
}

function normalizeRatio(value: unknown): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : 1
}

function buildAccountGroupRows(
  accountGroups: string,
  defaultUserGroup: string,
  topupGroupRatio: string,
  groupGroupRatio: string,
  groupSpecialUsableGroup: string
): AccountGroupRow[] {
  void defaultUserGroup
  void groupGroupRatio
  void groupSpecialUsableGroup

  const accountMap = parseStringMap(accountGroups)
  const topupMap = parseRatioMap(topupGroupRatio)

  return Object.keys(accountMap).map((name) => ({
    _id: createRowId('account'),
    name,
    description: accountMap[name] ?? '',
    topupRatio: Object.hasOwn(topupMap, name) ? String(topupMap[name]) : '',
  }))
}

function serializeAccountGroupRows(
  rows: AccountGroupRow[],
  preservedTopupGroupRatios: Record<string, number> = {}
) {
  const accountGroups: Record<string, string> = {}
  const rowNames = new Set(rows.map((row) => row.name.trim()).filter(Boolean))
  const topupGroupRatio: Record<string, number> = {}

  for (const [name, ratio] of Object.entries(preservedTopupGroupRatios)) {
    if (!rowNames.has(name)) topupGroupRatio[name] = ratio
  }

  for (const row of rows) {
    const name = row.name.trim()
    if (!name) continue
    accountGroups[name] = row.description
    const topupRatio = row.topupRatio.trim()
    if (
      topupRatio !== '' &&
      Number.isFinite(Number(topupRatio)) &&
      Number(topupRatio) >= 0
    ) {
      topupGroupRatio[name] = Number(topupRatio)
    }
  }

  return {
    AccountGroups: JSON.stringify(accountGroups, null, 2),
    TopupGroupRatio: JSON.stringify(topupGroupRatio, null, 2),
  }
}

function buildRouteGroupRows(
  groupRatio: string,
  userUsableGroups: string,
  autoGroups: string,
  groupGroupRatio: string,
  groupSpecialUsableGroup: string
): RouteGroupRow[] {
  void autoGroups
  void groupGroupRatio
  void groupSpecialUsableGroup

  const ratioMap = parseRatioMap(groupRatio)
  const usableMap = parseStringMap(userUsableGroups)

  return Object.keys(ratioMap).map((name) => ({
    _id: createRowId('route'),
    name,
    description: usableMap[name] ?? '',
    ratio: String(normalizeRatio(ratioMap[name])),
    selectable: Object.hasOwn(usableMap, name),
  }))
}

function serializeRouteGroupRows(
  rows: RouteGroupRow[],
  preservedUserUsableGroups: Record<string, string> = {}
) {
  const groupRatio: Record<string, number> = {}
  const rowNames = new Set(rows.map((row) => row.name.trim()).filter(Boolean))
  const userUsableGroups: Record<string, string> = {}

  for (const [name, description] of Object.entries(preservedUserUsableGroups)) {
    if (!rowNames.has(name)) userUsableGroups[name] = description
  }

  for (const row of rows) {
    const name = row.name.trim()
    if (!name) continue
    groupRatio[name] = normalizeRatio(row.ratio)
    if (row.selectable) userUsableGroups[name] = row.description
  }

  return {
    GroupRatio: JSON.stringify(groupRatio, null, 2),
    UserUsableGroups: JSON.stringify(userUsableGroups, null, 2),
  }
}

function parseAutoGroups(value: string): string[] {
  const parsed = safeJsonParse<unknown>(value, {
    fallback: [],
    silent: true,
  })
  return Array.isArray(parsed)
    ? parsed.filter((name): name is string => typeof name === 'string')
    : []
}

function accountRowsSignature(rows: AccountGroupRow[]) {
  return JSON.stringify(serializeAccountGroupRows(rows))
}

function routeRowsSignature(rows: RouteGroupRow[]) {
  return JSON.stringify(serializeRouteGroupRows(rows))
}

function UnknownBadge({ kind }: { kind: 'account' | 'route' }) {
  const { t } = useTranslation()
  return (
    <StatusBadge variant='danger' copyable={false}>
      <AlertTriangle className='mr-1 h-3 w-3' />
      {kind === 'account'
        ? t('Not in account group catalog')
        : t('Not in route group catalog')}
    </StatusBadge>
  )
}

type GroupNameSelectProps = {
  options: string[]
  value: string
  placeholder: string
  onValueChange: (value: string) => void
  className?: string
}

function GroupNameSelect(props: GroupNameSelectProps) {
  const options = useMemo(() => {
    if (props.value && !props.options.includes(props.value)) {
      return [props.value, ...props.options]
    }
    return props.options
  }, [props.options, props.value])

  return (
    <Select
      value={props.value === '' ? null : props.value}
      onValueChange={(value) => {
        if (typeof value === 'string' && value !== '') {
          props.onValueChange(value)
        }
      }}
    >
      <SelectTrigger className={props.className ?? 'w-48'}>
        <SelectValue placeholder={props.placeholder} />
      </SelectTrigger>
      <SelectContent alignItemWithTrigger={false}>
        <SelectGroup>
          {options.map((name) => (
            <SelectItem key={name} value={name}>
              {name}
            </SelectItem>
          ))}
        </SelectGroup>
      </SelectContent>
    </Select>
  )
}

export const GroupRatioVisualEditor = memo(function GroupRatioVisualEditor(
  props: GroupRatioVisualEditorProps
) {
  const { t } = useTranslation()
  const onChange = props.onChange
  const [accountRows, setAccountRows] = useState(() =>
    buildAccountGroupRows(
      props.accountGroups,
      props.defaultUserGroup,
      props.topupGroupRatio,
      props.groupGroupRatio,
      props.groupSpecialUsableGroup
    )
  )
  const [routeRows, setRouteRows] = useState(() =>
    buildRouteGroupRows(
      props.groupRatio,
      props.userUsableGroups,
      props.autoGroups,
      props.groupGroupRatio,
      props.groupSpecialUsableGroup
    )
  )

  useEffect(() => {
    const incomingRows = buildAccountGroupRows(
      props.accountGroups,
      props.defaultUserGroup,
      props.topupGroupRatio,
      props.groupGroupRatio,
      props.groupSpecialUsableGroup
    )
    setAccountRows((currentRows) =>
      accountRowsSignature(currentRows) === accountRowsSignature(incomingRows)
        ? currentRows
        : incomingRows
    )
  }, [
    props.accountGroups,
    props.defaultUserGroup,
    props.topupGroupRatio,
    props.groupGroupRatio,
    props.groupSpecialUsableGroup,
  ])

  useEffect(() => {
    const incomingRows = buildRouteGroupRows(
      props.groupRatio,
      props.userUsableGroups,
      props.autoGroups,
      props.groupGroupRatio,
      props.groupSpecialUsableGroup
    )
    setRouteRows((currentRows) =>
      routeRowsSignature(currentRows) === routeRowsSignature(incomingRows)
        ? currentRows
        : incomingRows
    )
  }, [
    props.groupRatio,
    props.userUsableGroups,
    props.autoGroups,
    props.groupGroupRatio,
    props.groupSpecialUsableGroup,
  ])

  const accountOptions = useMemo(
    () => accountRows.map((row) => row.name.trim()).filter(Boolean),
    [accountRows]
  )
  const routeOptions = useMemo(
    () => routeRows.map((row) => row.name.trim()).filter(Boolean),
    [routeRows]
  )
  const topupOnlyGroupRatios = useMemo(() => {
    const accountMap = parseStringMap(props.accountGroups)
    const topupMap = parseRatioMap(props.topupGroupRatio)
    return Object.fromEntries(
      Object.entries(topupMap).filter(
        ([name]) => !Object.hasOwn(accountMap, name)
      )
    )
  }, [props.accountGroups, props.topupGroupRatio])
  const routeOnlyUserUsableGroups = useMemo(() => {
    const routeMap = parseRatioMap(props.groupRatio)
    const usableMap = parseStringMap(props.userUsableGroups)
    return Object.fromEntries(
      Object.entries(usableMap).filter(
        ([name]) => !Object.hasOwn(routeMap, name)
      )
    )
  }, [props.groupRatio, props.userUsableGroups])

  const emitAccountRows = useCallback(
    (nextRows: AccountGroupRow[]) => {
      setAccountRows(nextRows)
      const serialized = serializeAccountGroupRows(
        nextRows,
        topupOnlyGroupRatios
      )
      onChange('AccountGroups', serialized.AccountGroups)
      onChange('TopupGroupRatio', serialized.TopupGroupRatio)
    },
    [onChange, topupOnlyGroupRatios]
  )

  const updateAccountRow = useCallback(
    (
      rowId: string,
      field: Exclude<keyof AccountGroupRow, '_id'>,
      value: string
    ) => {
      emitAccountRows(
        accountRows.map((row) =>
          row._id === rowId ? { ...row, [field]: value } : row
        )
      )
    },
    [accountRows, emitAccountRows]
  )

  const addAccountRow = useCallback(() => {
    const existingNames = new Set(accountRows.map((row) => row.name))
    let index = 1
    let name = `account_${index}`
    while (existingNames.has(name)) {
      index += 1
      name = `account_${index}`
    }
    emitAccountRows([
      ...accountRows,
      {
        _id: createRowId('account'),
        name,
        description: '',
        topupRatio: '',
      },
    ])
  }, [accountRows, emitAccountRows])

  const removeAccountRow = useCallback(
    (rowId: string) => {
      emitAccountRows(accountRows.filter((row) => row._id !== rowId))
    },
    [accountRows, emitAccountRows]
  )

  const emitRouteRows = useCallback(
    (nextRows: RouteGroupRow[]) => {
      setRouteRows(nextRows)
      const serialized = serializeRouteGroupRows(
        nextRows,
        routeOnlyUserUsableGroups
      )
      onChange('GroupRatio', serialized.GroupRatio)
      onChange('UserUsableGroups', serialized.UserUsableGroups)
    },
    [onChange, routeOnlyUserUsableGroups]
  )

  const updateRouteRow = useCallback(
    (
      rowId: string,
      field: Exclude<keyof RouteGroupRow, '_id'>,
      value: string | boolean
    ) => {
      emitRouteRows(
        routeRows.map((row) =>
          row._id === rowId ? { ...row, [field]: value } : row
        )
      )
    },
    [emitRouteRows, routeRows]
  )

  const addRouteRow = useCallback(() => {
    const existingNames = new Set(routeRows.map((row) => row.name))
    let index = 1
    let name = `route_${index}`
    while (existingNames.has(name)) {
      index += 1
      name = `route_${index}`
    }
    emitRouteRows([
      ...routeRows,
      {
        _id: createRowId('route'),
        name,
        description: '',
        ratio: '1',
        selectable: true,
      },
    ])
  }, [emitRouteRows, routeRows])

  const removeRouteRow = useCallback(
    (rowId: string) => {
      emitRouteRows(routeRows.filter((row) => row._id !== rowId))
    },
    [emitRouteRows, routeRows]
  )

  const autoGroupsList = useMemo(
    () => parseAutoGroups(props.autoGroups),
    [props.autoGroups]
  )
  const autoGroupCandidates = useMemo(
    () =>
      routeOptions.filter(
        (name) => name !== 'auto' && !autoGroupsList.includes(name)
      ),
    [autoGroupsList, routeOptions]
  )

  const handleAutoGroupAdd = useCallback(
    (name: string) => {
      if (name === 'auto' || autoGroupsList.includes(name)) return
      onChange('AutoGroups', JSON.stringify([...autoGroupsList, name], null, 2))
    },
    [autoGroupsList, onChange]
  )

  const handleAutoGroupDelete = useCallback(
    (index: number) => {
      onChange(
        'AutoGroups',
        JSON.stringify(
          autoGroupsList.filter((_, itemIndex) => itemIndex !== index),
          null,
          2
        )
      )
    },
    [autoGroupsList, onChange]
  )

  const handleAutoGroupMove = useCallback(
    (index: number, direction: 'up' | 'down') => {
      const newIndex = direction === 'up' ? index - 1 : index + 1
      if (newIndex < 0 || newIndex >= autoGroupsList.length) return
      const nextGroups = [...autoGroupsList]
      ;[nextGroups[index], nextGroups[newIndex]] = [
        nextGroups[newIndex],
        nextGroups[index],
      ]
      onChange('AutoGroups', JSON.stringify(nextGroups, null, 2))
    },
    [autoGroupsList, onChange]
  )

  return (
    <div className='space-y-4'>
      <AccountGroupsTable
        rows={accountRows}
        defaultUserGroup={props.defaultUserGroup}
        onDefaultChange={(value) => onChange('DefaultUserGroup', value)}
        onUpdate={updateAccountRow}
        onAdd={addAccountRow}
        onRemove={removeAccountRow}
      />

      <RouteGroupsTable
        rows={routeRows}
        onUpdate={updateRouteRow}
        onAdd={addRouteRow}
        onRemove={removeRouteRow}
      />

      <GroupOverrideRules
        accountOptions={accountOptions}
        routeOptions={routeOptions}
        groupGroupRatio={props.groupGroupRatio}
        routeRows={routeRows}
        onChange={onChange}
      />

      <Card className={sectionCardClassName}>
        <CardHeader className={sectionHeaderClassName}>
          <CardTitle>{t('Auto route assignment order')}</CardTitle>
          <CardDescription>
            {t(
              'AutoGroups contains route groups only. The system tries these route groups from top to bottom.'
            )}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className='space-y-4'>
            {props.maxTokenAutoGroupsField}
            <GroupNameSelect
              options={autoGroupCandidates}
              value=''
              placeholder={t('Add route group')}
              onValueChange={handleAutoGroupAdd}
            />
            {autoGroupsList.length > 0 && (
              <div className='space-y-2'>
                {autoGroupsList.map((routeGroup, index) => (
                  <div
                    key={routeGroup}
                    className='flex items-center gap-2 rounded-md border p-3'
                  >
                    <GripVertical className='text-muted-foreground h-4 w-4' />
                    <span className='font-medium'>{routeGroup}</span>
                    {!routeOptions.includes(routeGroup) && (
                      <UnknownBadge kind='route' />
                    )}
                    <div className='ml-auto flex gap-1'>
                      <Button
                        variant='ghost'
                        size='sm'
                        disabled={index === 0}
                        onClick={() => handleAutoGroupMove(index, 'up')}
                        aria-label={t('Move route group up')}
                      >
                        ↑
                      </Button>
                      <Button
                        variant='ghost'
                        size='sm'
                        disabled={index === autoGroupsList.length - 1}
                        onClick={() => handleAutoGroupMove(index, 'down')}
                        aria-label={t('Move route group down')}
                      >
                        ↓
                      </Button>
                      <Button
                        variant='ghost'
                        size='sm'
                        onClick={() => handleAutoGroupDelete(index)}
                        aria-label={t('Remove route group')}
                      >
                        <Trash2 className='h-4 w-4' />
                      </Button>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </CardContent>
      </Card>

      <GroupSpecialUsableRulesEditor
        value={props.groupSpecialUsableGroup}
        accountGroupOptions={accountOptions}
        routeGroupOptions={routeOptions}
        onChange={(value) => onChange('GroupSpecialUsableGroup', value)}
      />

      <Card className={sectionCardClassName}>
        <CardHeader className={sectionHeaderClassName}>
          <CardTitle>{t('Legacy auto-group compatibility')}</CardTitle>
          <CardDescription>
            {t(
              'DefaultUseAutoGroup is preserved for backend compatibility and is not an active visual control. Configure the account default and AutoGroups above.'
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className='text-muted-foreground text-sm'>
          {props.defaultUseAutoGroup
            ? t('Legacy DefaultUseAutoGroup is enabled.')
            : t('Legacy DefaultUseAutoGroup is disabled.')}
        </CardContent>
      </Card>
    </div>
  )
})

type AccountGroupsTableProps = {
  rows: AccountGroupRow[]
  defaultUserGroup: string
  onDefaultChange: (value: string) => void
  onUpdate: (
    rowId: string,
    field: Exclude<keyof AccountGroupRow, '_id'>,
    value: string
  ) => void
  onAdd: () => void
  onRemove: (rowId: string) => void
}

function AccountGroupsTable(props: AccountGroupsTableProps) {
  const { t } = useTranslation()
  const duplicateNames = useMemo(() => {
    const counts = new Map<string, number>()
    for (const row of props.rows) {
      const name = row.name.trim()
      if (name) counts.set(name, (counts.get(name) ?? 0) + 1)
    }
    return [...counts.entries()]
      .filter(([, count]) => count > 1)
      .map(([name]) => name)
  }, [props.rows])

  return (
    <Card className={sectionCardClassName}>
      <CardHeader className={sectionHeaderClassName}>
        <div className='flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between'>
          <div>
            <CardTitle>{t('Account groups')}</CardTitle>
            <CardDescription>
              {t(
                'Account groups identify User.Group accounts. Their descriptions, top-up ratios, and default account group are independent from route groups.'
              )}
            </CardDescription>
          </div>
          <Button onClick={props.onAdd} size='sm' className='sm:self-start'>
            <Plus className='mr-2 h-4 w-4' />
            {t('Add account group')}
          </Button>
        </div>
      </CardHeader>
      <CardContent>
        <StaticDataTable
          data={props.rows}
          getRowKey={(row) => row._id}
          emptyClassName='text-muted-foreground h-20 text-sm'
          emptyContent={t(
            'No account groups yet. Add an account group to get started.'
          )}
          columns={[
            {
              id: 'account-group',
              header: t('Account group ID'),
              className: 'min-w-40',
              cell: (row) => (
                <Input
                  value={row.name}
                  onChange={(event) =>
                    props.onUpdate(row._id, 'name', event.target.value)
                  }
                  aria-invalid={duplicateNames.includes(row.name.trim())}
                />
              ),
            },
            {
              id: 'description',
              header: t('Account description'),
              className: 'min-w-52',
              cell: (row) => (
                <Input
                  value={row.description}
                  placeholder={t('Account group description')}
                  onChange={(event) =>
                    props.onUpdate(row._id, 'description', event.target.value)
                  }
                />
              ),
            },
            {
              id: 'topup-ratio',
              header: t('Top-up ratio'),
              className: 'w-32',
              cell: (row) => (
                <Input
                  type='number'
                  min={0}
                  step={0.1}
                  value={row.topupRatio}
                  placeholder={t('Not set')}
                  onChange={(event) =>
                    props.onUpdate(row._id, 'topupRatio', event.target.value)
                  }
                />
              ),
            },
            {
              id: 'default',
              header: t('Default account group'),
              className: 'w-40',
              cell: (row) => (
                <label className='text-muted-foreground flex items-center gap-2 text-sm'>
                  <input
                    type='radio'
                    name='default-account-group'
                    value={row.name}
                    checked={
                      row.name.trim() !== '' &&
                      props.defaultUserGroup === row.name
                    }
                    onChange={() => props.onDefaultChange(row.name)}
                    disabled={row.name.trim() === ''}
                  />
                  {t('Use as default')}
                </label>
              ),
            },
            {
              id: 'actions',
              header: t('Actions'),
              className: 'text-right',
              cellClassName: 'text-right',
              cell: (row) => (
                <Button
                  variant='ghost'
                  size='sm'
                  onClick={() => props.onRemove(row._id)}
                  aria-label={t('Delete account group')}
                >
                  <Trash2 className='h-4 w-4' />
                </Button>
              ),
            },
          ]}
        />
        {duplicateNames.length > 0 && (
          <p className='text-destructive mt-3 text-sm'>
            {t('Duplicate account group IDs: {{names}}', {
              names: duplicateNames.join(', '),
            })}
          </p>
        )}
        {props.defaultUserGroup &&
          !props.rows.some((row) => row.name === props.defaultUserGroup) && (
            <div className='mt-3 flex items-center gap-2 text-sm'>
              <UnknownBadge kind='account' />
              <span>
                {t('Default account group reference: {{group}}', {
                  group: props.defaultUserGroup,
                })}
              </span>
            </div>
          )}
      </CardContent>
    </Card>
  )
}

type RouteGroupsTableProps = {
  rows: RouteGroupRow[]
  onUpdate: (
    rowId: string,
    field: Exclude<keyof RouteGroupRow, '_id'>,
    value: string | boolean
  ) => void
  onAdd: () => void
  onRemove: (rowId: string) => void
}

function RouteGroupsTable(props: RouteGroupsTableProps) {
  const { t } = useTranslation()
  const duplicateNames = useMemo(() => {
    const counts = new Map<string, number>()
    for (const row of props.rows) {
      const name = row.name.trim()
      if (name) counts.set(name, (counts.get(name) ?? 0) + 1)
    }
    return [...counts.entries()]
      .filter(([, count]) => count > 1)
      .map(([name]) => name)
  }, [props.rows])

  return (
    <Card className={sectionCardClassName}>
      <CardHeader className={sectionHeaderClassName}>
        <div className='flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between'>
          <div>
            <CardTitle>{t('Route and billing groups')}</CardTitle>
            <CardDescription>
              {t(
                'Route groups identify the channels and fixed Token groups used for billing. Base ratios and user-selectable route descriptions live here.'
              )}
            </CardDescription>
          </div>
          <Button onClick={props.onAdd} size='sm' className='sm:self-start'>
            <Plus className='mr-2 h-4 w-4' />
            {t('Add route group')}
          </Button>
        </div>
      </CardHeader>
      <CardContent>
        <StaticDataTable
          data={props.rows}
          getRowKey={(row) => row._id}
          emptyClassName='text-muted-foreground h-20 text-sm'
          emptyContent={t(
            'No route groups yet. Add a route group to get started.'
          )}
          columns={[
            {
              id: 'route-group',
              header: t('Route group ID'),
              className: 'min-w-40',
              cell: (row) => (
                <div className='flex items-center gap-2'>
                  <Input
                    value={row.name}
                    onChange={(event) =>
                      props.onUpdate(row._id, 'name', event.target.value)
                    }
                    aria-invalid={duplicateNames.includes(row.name.trim())}
                  />
                  {!row.name.trim() && <UnknownBadge kind='route' />}
                </div>
              ),
            },
            {
              id: 'route-description',
              header: t('Route description'),
              className: 'min-w-52',
              cell: (row) => (
                <Input
                  value={row.description}
                  placeholder={t('Route group description')}
                  onChange={(event) =>
                    props.onUpdate(row._id, 'description', event.target.value)
                  }
                />
              ),
            },
            {
              id: 'ratio',
              header: t('Base ratio'),
              className: 'w-32',
              cell: (row) => (
                <Input
                  type='number'
                  min={0}
                  step={0.1}
                  value={row.ratio}
                  onChange={(event) =>
                    props.onUpdate(row._id, 'ratio', event.target.value)
                  }
                />
              ),
            },
            {
              id: 'selectable',
              header: t('User-selectable'),
              className: 'w-32 text-center',
              cell: (row) => (
                <div className='flex justify-center'>
                  <Checkbox
                    checked={row.selectable}
                    onCheckedChange={(checked) =>
                      props.onUpdate(row._id, 'selectable', checked === true)
                    }
                    aria-label={t('User-selectable')}
                  />
                </div>
              ),
            },
            {
              id: 'actions',
              header: t('Actions'),
              className: 'text-right',
              cellClassName: 'text-right',
              cell: (row) => (
                <Button
                  variant='ghost'
                  size='sm'
                  onClick={() => props.onRemove(row._id)}
                  aria-label={t('Delete route group')}
                >
                  <Trash2 className='h-4 w-4' />
                </Button>
              ),
            },
          ]}
        />
        {duplicateNames.length > 0 && (
          <p className='text-destructive mt-3 text-sm'>
            {t('Duplicate route group IDs: {{names}}', {
              names: duplicateNames.join(', '),
            })}
          </p>
        )}
      </CardContent>
    </Card>
  )
}

type GroupOverrideRulesProps = {
  accountOptions: string[]
  routeOptions: string[]
  groupGroupRatio: string
  routeRows: RouteGroupRow[]
  onChange: (field: string, value: string) => void
}

function buildOverrideRows(value: string): OverrideRow[] {
  const map = parseNestedRatioMap(value)
  const rows: OverrideRow[] = []
  for (const [accountGroup, routes] of Object.entries(map)) {
    for (const [routeGroup, ratio] of Object.entries(routes)) {
      rows.push({
        _id: createRowId('override'),
        accountGroup,
        routeGroup,
        ratio: String(ratio),
      })
    }
  }
  return rows
}

function serializeOverrideRows(rows: OverrideRow[]) {
  const map: Record<string, Record<string, number>> = {}
  for (const row of rows) {
    const accountGroup = row.accountGroup.trim()
    const routeGroup = row.routeGroup.trim()
    if (!accountGroup || !routeGroup) continue
    if (!map[accountGroup]) map[accountGroup] = {}
    map[accountGroup][routeGroup] = normalizeRatio(row.ratio)
  }
  return JSON.stringify(map, null, 2)
}

function GroupOverrideRules(props: GroupOverrideRulesProps) {
  const { t } = useTranslation()
  const onChange = props.onChange
  const [rows, setRows] = useState<OverrideRow[]>(() =>
    buildOverrideRows(props.groupGroupRatio)
  )

  useEffect(() => {
    const incomingRows = buildOverrideRows(props.groupGroupRatio)
    setRows((currentRows) =>
      serializeOverrideRows(currentRows) === serializeOverrideRows(incomingRows)
        ? currentRows
        : incomingRows
    )
  }, [props.groupGroupRatio])

  const baseRatioByRoute = useMemo(() => {
    const ratios = new Map<string, number>()
    for (const row of props.routeRows) {
      ratios.set(row.name, normalizeRatio(row.ratio))
    }
    return ratios
  }, [props.routeRows])

  const emitRows = useCallback(
    (nextRows: OverrideRow[]) => {
      setRows(nextRows)
      onChange('GroupGroupRatio', serializeOverrideRows(nextRows))
    },
    [onChange]
  )

  const addRow = useCallback(() => {
    const routeGroup = props.routeOptions[0] ?? ''
    emitRows([
      ...rows,
      {
        _id: createRowId('override'),
        accountGroup: props.accountOptions[0] ?? '',
        routeGroup,
        ratio: String(baseRatioByRoute.get(routeGroup) ?? 1),
      },
    ])
  }, [
    baseRatioByRoute,
    emitRows,
    props.accountOptions,
    props.routeOptions,
    rows,
  ])

  const updateRow = useCallback(
    (
      rowId: string,
      field: Exclude<keyof OverrideRow, '_id'>,
      value: string
    ) => {
      emitRows(
        rows.map((row) =>
          row._id === rowId ? { ...row, [field]: value } : row
        )
      )
    },
    [emitRows, rows]
  )

  const removeRow = useCallback(
    (rowId: string) => emitRows(rows.filter((row) => row._id !== rowId)),
    [emitRows, rows]
  )

  return (
    <Card className={sectionCardClassName}>
      <CardHeader className={sectionHeaderClassName}>
        <div className='flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between'>
          <div>
            <CardTitle>{t('Account-to-route ratio overrides')}</CardTitle>
            <CardDescription>
              {t(
                'GroupGroupRatio maps an account group on the outside to a route group on the inside. The override replaces that route group base ratio.'
              )}
            </CardDescription>
          </div>
          <Button
            onClick={addRow}
            size='sm'
            disabled={
              props.accountOptions.length === 0 ||
              props.routeOptions.length === 0
            }
          >
            <Plus className='mr-2 h-4 w-4' />
            {t('Add account-to-route override')}
          </Button>
        </div>
      </CardHeader>
      <CardContent>
        {rows.length === 0 ? (
          <p className='text-muted-foreground py-4 text-center text-sm'>
            {t('No account-to-route overrides yet.')}
          </p>
        ) : (
          <StaticDataTable
            data={rows}
            getRowKey={(row) => row._id}
            columns={[
              {
                id: 'account-group',
                header: t('Account group'),
                className: 'min-w-44',
                cell: (row) => (
                  <div className='flex items-center gap-2'>
                    <GroupNameSelect
                      options={props.accountOptions}
                      value={row.accountGroup}
                      placeholder={t('Select account group')}
                      onValueChange={(value) =>
                        updateRow(row._id, 'accountGroup', value)
                      }
                      className='w-full'
                    />
                    {!props.accountOptions.includes(row.accountGroup) && (
                      <UnknownBadge kind='account' />
                    )}
                  </div>
                ),
              },
              {
                id: 'route-group',
                header: t('Route group'),
                className: 'min-w-44',
                cell: (row) => (
                  <div className='flex items-center gap-2'>
                    <GroupNameSelect
                      options={props.routeOptions}
                      value={row.routeGroup}
                      placeholder={t('Select route group')}
                      onValueChange={(value) =>
                        updateRow(row._id, 'routeGroup', value)
                      }
                      className='w-full'
                    />
                    {!props.routeOptions.includes(row.routeGroup) && (
                      <UnknownBadge kind='route' />
                    )}
                  </div>
                ),
              },
              {
                id: 'ratio',
                header: t('Override ratio'),
                className: 'w-36',
                cell: (row) => {
                  const baseRatio = baseRatioByRoute.get(row.routeGroup)
                  return (
                    <div className='space-y-1'>
                      <Input
                        type='number'
                        min={0}
                        step={0.1}
                        value={row.ratio}
                        onChange={(event) =>
                          updateRow(row._id, 'ratio', event.target.value)
                        }
                      />
                      {baseRatio !== undefined && (
                        <span className='text-muted-foreground text-xs'>
                          {t('Base: {{ratio}}', { ratio: baseRatio })}
                        </span>
                      )}
                    </div>
                  )
                },
              },
              {
                id: 'actions',
                header: t('Actions'),
                className: 'text-right',
                cellClassName: 'text-right',
                cell: (row) => (
                  <Button
                    variant='ghost'
                    size='sm'
                    onClick={() => removeRow(row._id)}
                    aria-label={t('Delete account-to-route override')}
                  >
                    <Trash2 className='h-4 w-4' />
                  </Button>
                ),
              },
            ]}
          />
        )}
      </CardContent>
    </Card>
  )
}
