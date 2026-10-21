<script setup lang="ts">
// Inputs & Forms. Local refs back every v-model so the controls are
// interactive on the wall (drag sliders, type, add tags).
import { nextTick, ref } from 'vue'
import {
  Checkbox,
  Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList,
  Field, FieldContent, FieldControl, FieldDescription, FieldError, FieldGroup,
  FieldLabel, FieldLegend, FieldSeparator, FieldSet,
  Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage,
  Input,
  InputGroup, InputGroupAddon, InputGroupButton, InputGroupInput, InputGroupText, InputGroupTextarea,
  InputOTP, InputOTPGroup, InputOTPSeparator, InputOTPSlot,
  Label,
  NativeSelect, NativeSelectOptGroup, NativeSelectOption,
  NumberField,
  PinInput, PinInputGroup, PinInputSeparator, PinInputSlot,
  Popover, PopoverContent, PopoverTrigger,
  RadioGroup, RadioGroupItem,
  Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectSeparator, SelectTrigger, SelectValue,
  Switch,
  Slider,
  TagsInput, TagsInputInput, TagsInputItem, TagsInputItemDelete, TagsInputItemText,
  Textarea,
  Button,
  menuChromeClass,
  selectTriggerClass,
} from '@felinic/ui'
import { Check, ChevronsUpDown, Eye, EyeOff, Search, X } from 'lucide-vue-next'
import SectionShell from '../components/SectionShell.vue'
import Specimen from '../components/Specimen.vue'

const text = ref('Editable value')
const area = ref('Multiline\ntext')
const checked = ref(true)
const switched = ref(true)
const sliderSingle = ref([40])
const sliderRange = ref([20, 70])
const radioVal = ref('comfortable')
const nativeVal = ref('banana')
const selectVal = ref('')
const tags = ref(['vue', 'tailwind'])
const pin = ref<string[]>([])
const otp = ref('')

const comboItems = ['Apple', 'Banana', 'Cherry', 'Dragonfruit']
const comboVal = ref('')
const comboOpen = ref(false)
// Multi-select: same Popover + Command surface, but Command runs in `multiple` mode so
// the model is an array and picking a row toggles it without closing the panel.
const comboMultiVal = ref<string[]>(['Apple'])
const comboMultiOpen = ref(false)

const clearable = ref('Clear me')
const password = ref('hunter2')
const showPassword = ref(false)

const quantity = ref(2)
const notify = ref(true)

const formSchema = {
  email: (value: string) => (value ? true : 'Email is required'),
}
</script>

<template>
  <SectionShell
    id="inputs-forms"
    label="Inputs & Forms"
    description="Text fields, choosers, and the vee-validate form stack."
  >
    <!-- Input system — edge emphasis (2 states). Same field; focus only.
         'solid' is the everyday default; 'subtle' is for search / low-chrome. -->
    <div class="mb-6 grid grid-cols-1 gap-4 sm:grid-cols-2">
      <Specimen
        label="emphasis solid (default)"
        note="focus turns the edge black"
      >
        <Input
          placeholder="Project name"
          class="w-full"
        />
      </Specimen>
      <Specimen
        label="emphasis subtle"
        note="focus barely deepens the edge"
      >
        <Input
          emphasis="subtle"
          placeholder="Search library"
          class="w-full"
        />
      </Specimen>
    </div>

    <!-- Size scale + composition recipes. These are the usage patterns we WANT
         people to copy, so their styling is tuned here once instead of ad-hoc. -->
    <div class="mb-6 grid grid-cols-1 gap-4 lg:grid-cols-3">
      <Specimen
        label="size scale"
        note="sm · default · lg"
      >
        <div class="flex w-full flex-col gap-2">
          <Input
            size="sm"
            placeholder="Small (sm) — 32px"
          />
          <Input placeholder="Default — 36px" />
          <Input
            size="lg"
            placeholder="Large (lg) — 40px"
          />
        </div>
      </Specimen>

      <Specimen
        label="<Field> wrapper"
        note="auto-wires id / for / aria-* — no manual ids"
      >
        <div class="flex w-full flex-col gap-4">
          <Field>
            <FieldLabel required>
              Email
            </FieldLabel>
            <FieldControl>
              <Input
                type="email"
                placeholder="you@example.com"
              />
            </FieldControl>
            <FieldDescription>We'll never share it.</FieldDescription>
          </Field>
          <Field invalid>
            <FieldLabel optional>
              Email
            </FieldLabel>
            <FieldControl>
              <Input model-value="11" />
            </FieldControl>
            <FieldError>Email is not valid.</FieldError>
          </Field>
        </div>
      </Specimen>

      <Specimen
        label="recipes"
        note="clearable · password reveal (InputGroup)"
      >
        <div class="flex w-full flex-col gap-2">
          <InputGroup>
            <InputGroupInput
              v-model="clearable"
              placeholder="Type to clear..."
            />
            <InputGroupAddon
              v-if="clearable"
              align="inline-end"
            >
              <InputGroupButton
                size="icon-xs"
                aria-label="Clear"
                @click="clearable = ''"
              >
                <X />
              </InputGroupButton>
            </InputGroupAddon>
          </InputGroup>
          <InputGroup>
            <InputGroupInput
              v-model="password"
              :type="showPassword ? 'text' : 'password'"
              placeholder="Password"
            />
            <InputGroupAddon align="inline-end">
              <InputGroupButton
                size="icon-xs"
                variant="quiet"
                :aria-label="showPassword ? 'Hide password' : 'Show password'"
                @click="showPassword = !showPassword"
              >
                <EyeOff v-if="showPassword" />
                <Eye v-else />
              </InputGroupButton>
            </InputGroupAddon>
          </InputGroup>
        </div>
      </Specimen>
    </div>

    <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <Specimen label="<Input> states">
        <div class="flex w-full max-w-xs flex-col gap-2">
          <Input v-model="text" />
          <Input placeholder="Placeholder" />
          <Input
            disabled
            placeholder="Disabled"
          />
          <div class="flex flex-col gap-1.5">
            <Input
              aria-invalid="true"
              model-value="11"
            />
            <FieldError>Email is not valid.</FieldError>
          </div>
        </div>
      </Specimen>

      <Specimen label="<Textarea>">
        <Textarea
          v-model="area"
          class="w-full"
        />
      </Specimen>

      <Specimen label="<Checkbox>">
        <Label class="flex items-center gap-2 text-label">
          <Checkbox v-model="checked" /> Checked
        </Label>
        <Label class="flex items-center gap-2 text-label">
          <Checkbox :model-value="false" /> Unchecked
        </Label>
        <Label class="flex items-center gap-2 text-label opacity-60">
          <Checkbox disabled /> Disabled
        </Label>
      </Specimen>

      <Specimen
        label="<Switch>"
        note="on = palette blue · off = gray · hover deepens/brightens · sizes sm / default"
      >
        <div class="flex flex-col gap-3">
          <div class="flex items-center gap-3">
            <Switch
              :default-value="true"
              size="sm"
            />
            <Switch
              :model-value="false"
              size="sm"
            />
            <span class="text-[11px] text-muted-foreground">sm · 32×20</span>
          </div>
          <div class="flex items-center gap-3">
            <Switch v-model="switched" />
            <Switch :model-value="false" />
            <span class="text-[11px] text-muted-foreground">default · 36×20</span>
          </div>
          <div class="flex items-center gap-3">
            <Switch
              :default-value="true"
              disabled
            />
            <Switch
              :model-value="false"
              disabled
            />
            <span class="text-[11px] text-muted-foreground">disabled</span>
          </div>
        </div>
      </Specimen>

      <Specimen
        label="<NumberField>"
        note="ghost steppers · same field edge · sizes"
      >
        <div class="flex w-full flex-col gap-2">
          <NumberField
            v-model="quantity"
            :min="0"
            :max="10"
            class="w-40"
          />
          <NumberField
            :default-value="5"
            size="sm"
            class="w-32"
          />
          <NumberField
            :default-value="1"
            disabled
            class="w-32"
          />
        </div>
      </Specimen>

      <Specimen label="<Slider> single / range / disabled">
        <div class="flex w-full flex-col gap-4">
          <Slider
            v-model="sliderSingle"
            :min="0"
            :max="100"
          />
          <Slider
            v-model="sliderRange"
            :min="0"
            :max="100"
          />
          <Slider
            :model-value="[50]"
            disabled
          />
        </div>
      </Specimen>

      <Specimen
        label="<RadioGroup>"
        note="select grows the edge — thin gray ring animates into a thick blue ring (and back)"
      >
        <RadioGroup
          v-model="radioVal"
          class="gap-2"
        >
          <Label class="flex items-center gap-2 text-label">
            <RadioGroupItem value="comfortable" /> Comfortable
          </Label>
          <Label class="flex items-center gap-2 text-label">
            <RadioGroupItem value="compact" /> Compact
          </Label>
          <Label class="flex items-center gap-2 text-label">
            <RadioGroupItem value="spacious" /> Spacious
          </Label>
          <Label class="flex items-center gap-2 text-label opacity-60">
            <RadioGroupItem
              value="disabled"
              disabled
            /> Disabled
          </Label>
        </RadioGroup>
      </Specimen>

      <Specimen label="<NativeSelect>">
        <NativeSelect
          v-model="nativeVal"
          class="w-48"
        >
          <NativeSelectOptGroup label="Fruits">
            <NativeSelectOption value="apple">
              Apple
            </NativeSelectOption>
            <NativeSelectOption value="banana">
              Banana
            </NativeSelectOption>
          </NativeSelectOptGroup>
          <NativeSelectOption value="carrot">
            Carrot
          </NativeSelectOption>
        </NativeSelect>
      </Specimen>

      <Specimen label="<Select>">
        <Select v-model="selectVal">
          <SelectTrigger class="w-48">
            <SelectValue placeholder="Pick a fruit" />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectLabel>Fruits</SelectLabel>
              <SelectItem value="apple">
                Apple
              </SelectItem>
              <SelectItem value="banana">
                Banana
              </SelectItem>
              <SelectSeparator />
              <SelectItem value="cherry">
                Cherry
              </SelectItem>
            </SelectGroup>
          </SelectContent>
        </Select>
      </Specimen>

      <Specimen
        label="<Combobox>"
        note="Popover + Command — select-style trigger, search lives in the panel"
      >
        <Popover v-model:open="comboOpen">
          <PopoverTrigger as-child>
            <button
              data-slot="select-trigger"
              data-size="default"
              type="button"
              :data-placeholder="comboVal ? undefined : ''"
              :class="[selectTriggerClass, 'w-56']"
            >
              <span class="line-clamp-1">{{ comboVal || 'Select fruit' }}</span>
              <ChevronsUpDown class="opacity-50" />
            </button>
          </PopoverTrigger>
          <PopoverContent
            menu
            align="start"
            class="w-[var(--reka-popover-trigger-width)]"
          >
            <Command
              v-model="comboVal"
              highlight-on-hover
              :highlight-first-on-open="false"
              :class="menuChromeClass"
              @update:model-value="() => nextTick(() => (comboOpen = false))"
            >
              <CommandInput
                :search-icon="false"
                size="md"
                placeholder="Search fruit..."
                class="placeholder:text-muted-foreground/80"
              />
              <CommandList>
                <CommandEmpty>No fruit found.</CommandEmpty>
                <CommandGroup>
                  <CommandItem
                    v-for="item in comboItems"
                    :key="item"
                    :value="item"
                  >
                    {{ item }}
                    <Check
                      v-if="comboVal === item"
                      class="ml-auto"
                    />
                  </CommandItem>
                </CommandGroup>
              </CommandList>
            </Command>
          </PopoverContent>
        </Popover>
      </Specimen>

      <Specimen
        label="<Combobox> · multiple"
        note="Command in multiple mode — array model, toggle rows, panel stays open"
      >
        <Popover v-model:open="comboMultiOpen">
          <PopoverTrigger as-child>
            <button
              data-slot="select-trigger"
              data-size="default"
              type="button"
              :data-placeholder="comboMultiVal.length ? undefined : ''"
              :class="[selectTriggerClass, 'w-56']"
            >
              <span class="line-clamp-1">{{ comboMultiVal.length ? comboMultiVal.join(', ') : 'Select fruits' }}</span>
              <ChevronsUpDown class="opacity-50" />
            </button>
          </PopoverTrigger>
          <PopoverContent
            menu
            align="start"
            class="w-[var(--reka-popover-trigger-width)]"
          >
            <Command
              v-model="comboMultiVal"
              multiple
              highlight-on-hover
              :highlight-first-on-open="false"
              :class="menuChromeClass"
            >
              <CommandInput
                :search-icon="false"
                size="md"
                placeholder="Search fruit..."
                class="placeholder:text-muted-foreground/80"
              />
              <CommandList>
                <CommandEmpty>No fruit found.</CommandEmpty>
                <CommandGroup>
                  <CommandItem
                    v-for="item in comboItems"
                    :key="item"
                    :value="item"
                  >
                    {{ item }}
                    <Check
                      v-if="comboMultiVal.includes(item)"
                      class="ml-auto"
                    />
                  </CommandItem>
                </CommandGroup>
              </CommandList>
            </Command>
          </PopoverContent>
        </Popover>
      </Specimen>

      <Specimen label="<TagsInput>">
        <TagsInput
          v-model="tags"
          class="w-64"
        >
          <TagsInputItem
            v-for="item in tags"
            :key="item"
            :value="item"
          >
            <TagsInputItemText />
            <TagsInputItemDelete />
          </TagsInputItem>
          <TagsInputInput placeholder="Add tag..." />
        </TagsInput>
      </Specimen>

      <Specimen label="<PinInput>">
        <PinInput v-model="pin">
          <PinInputGroup>
            <PinInputSlot :index="0" />
            <PinInputSlot :index="1" />
          </PinInputGroup>
          <PinInputSeparator />
          <PinInputGroup>
            <PinInputSlot :index="2" />
            <PinInputSlot :index="3" />
          </PinInputGroup>
        </PinInput>
      </Specimen>

      <Specimen label="<InputOTP :maxlength>">
        <InputOTP
          v-model="otp"
          :maxlength="6"
        >
          <template #default="{ slots }">
            <InputOTPGroup>
              <InputOTPSlot
                v-for="(_slot, i) in slots.slice(0, 3)"
                :key="i"
                :index="i"
              />
            </InputOTPGroup>
            <InputOTPSeparator />
            <InputOTPGroup>
              <InputOTPSlot
                v-for="(_slot, i) in slots.slice(3, 6)"
                :key="i + 3"
                :index="i + 3"
              />
            </InputOTPGroup>
          </template>
        </InputOTP>
      </Specimen>

      <Specimen label="<InputGroup>">
        <div class="flex w-full flex-col gap-2">
          <InputGroup>
            <InputGroupAddon>
              <Search class="size-4" />
            </InputGroupAddon>
            <InputGroupInput placeholder="Search..." />
            <InputGroupButton>Go</InputGroupButton>
          </InputGroup>
          <InputGroup>
            <InputGroupInput placeholder="https://" />
            <InputGroupAddon align="inline-end">
              <InputGroupText>.com</InputGroupText>
            </InputGroupAddon>
          </InputGroup>
          <InputGroup>
            <InputGroupTextarea placeholder="Multiline group..." />
          </InputGroup>
        </div>
      </Specimen>

      <div class="lg:col-span-2">
        <Specimen
          label="<FieldSet> + <FieldGroup> (semantic grouping)"
          note="legend · stacked fields · separator · horizontal row"
        >
          <FieldSet class="w-full max-w-md">
            <FieldLegend>Notifications</FieldLegend>
            <FieldGroup>
              <Field>
                <FieldLabel required>
                  Display name
                </FieldLabel>
                <FieldControl>
                  <Input placeholder="Ada Lovelace" />
                </FieldControl>
                <FieldDescription>Shown on your public profile.</FieldDescription>
              </Field>

              <FieldSeparator>then</FieldSeparator>

              <Field orientation="horizontal">
                <FieldContent>
                  <FieldLabel>Email notifications</FieldLabel>
                  <FieldDescription>Get a digest whenever something changes.</FieldDescription>
                </FieldContent>
                <Switch v-model="notify" />
              </Field>
            </FieldGroup>
          </FieldSet>
        </Specimen>
      </div>

      <div class="lg:col-span-2">
        <Specimen
          label="<Form> + <FormField> (vee-validate)"
          note="submit empty to see FormMessage"
        >
          <Form
            class="w-full max-w-sm space-y-3"
            :validation-schema="formSchema"
            :initial-values="{ email: '' }"
            @submit="() => {}"
          >
            <FormField
              v-slot="{ componentField }"
              name="email"
            >
              <FormItem>
                <FormLabel>Email</FormLabel>
                <FormControl>
                  <Input
                    type="email"
                    placeholder="you@example.com"
                    v-bind="componentField"
                  />
                </FormControl>
                <FormDescription>We'll never share it.</FormDescription>
                <FormMessage />
              </FormItem>
            </FormField>
            <Button
              type="submit"
              size="sm"
            >
              Submit
            </Button>
          </Form>
        </Specimen>
      </div>
    </div>
  </SectionShell>
</template>
