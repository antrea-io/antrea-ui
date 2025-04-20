/**
 * Copyright 2023 Antrea Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import { useState, useEffect, useRef} from 'react';
import { useNavigate, Outlet } from "react-router";
import { useForm, SubmitHandler } from "react-hook-form";
import { CdsAlertGroup, CdsAlert } from "@cds/react/alert";
import { CdsButton } from '@cds/react/button';
import { CdsCheckbox } from '@cds/react/checkbox';
import { CdsFormGroup, CdsControlMessage } from '@cds/react/forms';
import { CdsInput } from "@cds/react/input";
import { CdsRadioGroup, CdsRadio } from '@cds/react/radio';
import { CdsSelect } from '@cds/react/select';
import { ErrorMessage } from '@hookform/error-message';
import { ErrorMessageContainer } from '../components/form-errors';
import { isIP, ipVersion } from 'is-ip';
import { TraceflowPacket, TraceflowSpec, traceflowAPI } from '../api/traceflow';
import { useAppError} from '../components/errors';

type Inputs = {
    srcNamespace: string
    src: string
    srcPort: number
    destinationType: string
    dstNamespace: string
    dst: string
    dstPort: number
    protocol: string
    timeout: number
    useIPv6: boolean
    liveTraffic: boolean
    droppedOnly: boolean
    tcpFlags: number
};

function createTraceflowPacket(inputs: Inputs, useIPv6: boolean): TraceflowPacket {
    const packet: TraceflowPacket = {
        transportHeader: {},
    };
    let protocol = 0;
    switch (inputs.protocol) {
            case "ICMP": {
                if (useIPv6) {
                    protocol = 58;
                } else {
                    protocol = 1;
                }
                packet.transportHeader.icmp = {};
                break;
            }
            case "TCP": {
                protocol = 6;
                packet.transportHeader.tcp = {
                    flags: inputs.tcpFlags,
                };
                // A srcPort or dstPort equal to 0 (which has a specific meaning) must be omitted
                // from the Traceflow API request (since v1beta1), or the request will be rejected.
                if (inputs.srcPort > 0) {
                    packet.transportHeader.tcp.srcPort = inputs.srcPort;
                }
                if (inputs.dstPort > 0) {
                    packet.transportHeader.tcp.dstPort = inputs.dstPort;
                }
                break;
            }
            case "UDP": {
                protocol = 17;
                packet.transportHeader.udp = {};
                if (inputs.srcPort > 0) {
                    packet.transportHeader.udp.srcPort = inputs.srcPort;
                }
                if (inputs.dstPort > 0) {
                    packet.transportHeader.udp.dstPort = inputs.dstPort;
                }
                break;
            }
    }

    if (useIPv6) {
        packet.ipv6Header = {
            nextHeader: protocol,
        };
    } else {
        packet.ipHeader = {
            protocol: protocol,
        };
    }

    return packet;
}

function createTraceflowRequest(inputs: Inputs): TraceflowSpec {
    if (inputs.droppedOnly && !inputs.liveTraffic) {
        throw new Error("droppedOnly can only be used for live traffic Traceflow");
    }
    if (!inputs.src && !inputs.liveTraffic) {
        throw new Error("missing source");
    }
    if (!inputs.dst && !inputs.liveTraffic) {
        throw new Error("missing source");
    }
    if (!inputs.src && !inputs.dst) {
        throw new Error("at least one of source and destination is required");
    }

    const sourceType = isIP(inputs.src) ? "IP" : "Pod";
    if (sourceType === "IP" && !inputs.liveTraffic) {
        throw new Error("source must be a Pod for a normal Traceflow");
    }
    if (sourceType !== "Pod" && inputs.destinationType !== "Pod") {
        throw new Error("at least one of source and destination must be a Pod");
    }

    const dstIPVersion = ipVersion(inputs.dst);
    const srcIPVersion = ipVersion(inputs.src);
    if (srcIPVersion && dstIPVersion && (srcIPVersion !== dstIPVersion)) {
        throw new Error("IP version mismatch between source and destination");
    }
    
    if (srcIPVersion === 4 && inputs.useIPv6) {
        throw new Error("do not check the 'Use IPv6' box when providing an IPv4 source address");
    }
    if (dstIPVersion === 4 && inputs.useIPv6) {
        throw new Error("do not check the 'Use IPv6' box when providing an IPv4 destination address");
    }
    const useIPv6 = (dstIPVersion === 6 || srcIPVersion === 6 || inputs.useIPv6);

    const spec: TraceflowSpec = {
        source: {},
        destination: {},
    };
    if (inputs.src) {
        if (sourceType === "IP") {
            spec.source.ip = inputs.src;
        } else {
            spec.source.namespace = inputs.srcNamespace;
            spec.source.pod = inputs.src;
        }
    }
    if (inputs.destinationType && inputs.dst) {
        switch (inputs.destinationType) {
            case "Pod": {
                spec.destination.namespace = inputs.dstNamespace;
                spec.destination.pod = inputs.dst;
                break;
            }
            case "Service": {
                spec.destination.namespace = inputs.dstNamespace;
                spec.destination.service = inputs.dst;
                break;
            }
            case "IP": {
                if (!isIP(inputs.dst)) {
                    throw new Error("invalid destination IP address");
                }
                spec.destination.ip = inputs.dst;
            }
        }
    }

    spec.packet = createTraceflowPacket(inputs, useIPv6);
    if (inputs.liveTraffic) spec.liveTraffic = inputs.liveTraffic;
    if (inputs.droppedOnly) spec.droppedOnly = inputs.droppedOnly;
    spec.timeout = inputs.timeout;

    return spec;
}

function TraceflowRunningAlert(props: { traceflowRunning: boolean }) {
    const traceflowRunning = props.traceflowRunning;

    if (!traceflowRunning) return null;

    return (
        <CdsAlertGroup type="banner" status="info">
            <CdsAlert status="loading">Running Traceflow, it could take a few seconds</CdsAlert>
        </CdsAlertGroup>
    );
}

export default function Traceflow() {
    function defaultValues(liveTraffic: boolean, protocol: string): Partial<Inputs> {
        let dstPort = 0;
        if (!liveTraffic) {
            if (protocol === "TCP") dstPort = 80;
            else if (protocol === "UDP") dstPort = 53;
        }
        let tcpFlags = 0;
        if (protocol === "TCP" && !liveTraffic) {
            tcpFlags = 2;
        }
        return {
            srcNamespace: "default",
            src: "",
            srcPort: 0,
            destinationType: "Pod",
            dstNamespace: "default",
            dst: "",
            dstPort: dstPort,
            protocol: protocol,
            timeout: 20,
            tcpFlags: tcpFlags,
            useIPv6: false,
            liveTraffic: liveTraffic,
            droppedOnly: false,
        };
    }

    const { 
        register, 
        handleSubmit, 
        reset, 
        watch, 
        unregister,
        setValue,
        formState: { errors },
        trigger
    } = useForm<Inputs>({
        defaultValues: defaultValues(false, "TCP"),
        mode: "onChange"
    });

    const navigate = useNavigate();
    const [traceflowRunning, setTraceflowRunning] = useState<boolean>(false);
    const mountedRef = useRef<boolean>(false);
    const { addError, removeError } = useAppError();

    // Watch form values for dynamic updates
    const protocol = watch("protocol");
    const isLiveTraffic = watch("liveTraffic");
    const useIPv6 = watch("useIPv6");

    // Custom validation function
    const validateForm = (values: Inputs): Record<string, string> => {
        const errors: Record<string, string> = {};
        
        if (values.droppedOnly && !values.liveTraffic) {
            errors.droppedOnly = "Dropped traffic can only be used for live traffic Traceflow";
        }
        
        if (!values.src && !values.liveTraffic) {
            errors.src = "Source is required for non-live traffic";
        }

        if (!values.dst && !values.liveTraffic) {
            errors.dst = "Destination is required for non-live traffic";
        }

        if (!values.src && !values.dst) {
            errors.src = "At least one of source and destination is required";
            errors.dst = "At least one of source and destination is required";
        }

        const sourceType = isIP(values.src) ? "IP" : "Pod";
        if (sourceType === "IP" && !values.liveTraffic) {
            errors.src = "Source must be a Pod for a normal Traceflow";
        }
        if (sourceType !== "Pod" && values.destinationType !== "Pod") {
            errors.src = "At least one of source and destination must be a Pod";
            errors.destinationType = "At least one of source and destination must be a Pod";
        }

        const dstIPVersion = ipVersion(values.dst);
        const srcIPVersion = ipVersion(values.src);
        if (srcIPVersion && dstIPVersion && (srcIPVersion !== dstIPVersion)) {
            errors.src = "IP version mismatch between source and destination";
            errors.dst = "IP version mismatch between source and destination";
        }
        
        if (srcIPVersion === 4 && values.useIPv6) {
            errors.useIPv6 = "Do not check the 'Use IPv6' box when providing an IPv4 source address";
        }
        if (dstIPVersion === 4 && values.useIPv6) {
            errors.useIPv6 = "Do not check the 'Use IPv6' box when providing an IPv4 destination address";
        }

        return errors;
    };

    // Handle protocol changes
    useEffect(() => {
        if (protocol === "TCP") {
            setValue("dstPort", isLiveTraffic ? 0 : 80);
            setValue("tcpFlags", isLiveTraffic ? 0 : 2);
        } else if (protocol === "UDP") {
            setValue("dstPort", isLiveTraffic ? 0 : 53);
            setValue("tcpFlags", 0);
        } else if (protocol === "ICMP") {
            unregister(["srcPort", "dstPort", "tcpFlags"]);
        }
        trigger();
    }, [protocol, isLiveTraffic]);

    // Handle live traffic changes
    useEffect(() => {
        if (isLiveTraffic) {
            setValue("dstPort", 0);
            setValue("tcpFlags", 0);
        } else {
            if (protocol === "TCP") {
                setValue("dstPort", 80);
                setValue("tcpFlags", 2);
            } else if (protocol === "UDP") {
                setValue("dstPort", 53);
            }
        }
        trigger();
    }, [isLiveTraffic]);

    const srcPort = register(
        "srcPort",
        {
            min: {
                value: 0,
                message: "Source port must be >= 0",
            },
            max: {
                value: 65535,
                message: "Source port must be <= 65535",
            },
            setValueAs: parseInt,
        },
    );

    const destinationType = register(
        "destinationType",
        {
            required: !isLiveTraffic,
        },
    );

    const dstPortRequired = !isLiveTraffic && (protocol === "TCP" || protocol === "UDP");
    const dstPort = register(
        "dstPort",
        {
            min: {
                value: dstPortRequired ? 1 : 0,
                message: "Destination port must be >= " +  (dstPortRequired ? 1 : 0).toString(),
            },
            max: {
                value: 65535,
                message: "Destination port must be <= 65535",
            },
            setValueAs: parseInt,
            required: dstPortRequired && "Destination port is required",
        },
    );

    const tcpFlags = register(
        "tcpFlags",
        {
            min: {
                value: 0,
                message: "TCP flags must be >= 0",
            },
            max: {
                value: 255,
                message: "TCP flags must be <= 255",
            },
            setValueAs: parseInt,
        },
    );

    const timeout = register(
        "timeout",
        {
            min: {
                value: 1,
                message: "Timeout must be >= 1",
            },
            max: {
                value: 120,
                message: "Timeout must be <= 120",
            },
            setValueAs: parseInt,
        },
    );

    const handleReset = () => {
        reset(defaultValues(false, "TCP"), {
            keepDefaultValues: true,
            keepDirty: false
        });
        navigate("/traceflow");
    };

    async function runTraceflow(tf: TraceflowSpec, cb: () => void) {
        try {
            const tfStatus = await traceflowAPI.runTraceflow(tf, true);
            if (tfStatus === undefined) {
                throw new Error("missing Traceflow status");
            }
            navigate(`/traceflow/result`, {
                state: {
                    spec: tf,
                    status: tfStatus,
                },
            });
        } catch (e) {
            // not sure whether this is the best way to do this, but we want to
            // remove the graph if present
            navigate(`/traceflow`);
            if (e instanceof Error) addError(e);
            console.error(e);
        }
        if (mountedRef.current) {
            cb();
        }
    }

    const onSubmit: SubmitHandler<Inputs> = async (data) => {
        removeError();
        const validationErrors = validateForm(data);
        if (Object.keys(validationErrors).length > 0) {
            Object.entries(validationErrors).forEach(([field, message]) => {
                addError(new Error(`${field}: ${message}`));
            });
            return;
        }

        let tf: TraceflowSpec;
        try {
            tf = createTraceflowRequest(data);
        } catch (e) {
            addError(e as Error);
            return;
        }
        setTraceflowRunning(true);
        runTraceflow(tf, () => {
            setTraceflowRunning(false);
        });
    };

    useEffect(() => {
        mountedRef.current = true;
        return () => {
            mountedRef.current = false;
        };
    }, []);

    return (
        <main>
            <div cds-layout="horizontal gap:lg">
            <div cds-layout="vertical gap:lg">
                <p cds-text="title">Traceflow</p>
                <form onSubmit = {handleSubmit(onSubmit)}>
                    <CdsFormGroup layout="horizontal">
                        <CdsInput>
                            <label>Source Namespace</label>
                            <input {...register("srcNamespace")} />
                        </CdsInput>
                        <CdsInput>
                            <label>Source</label>
                            <input {...register("src")} placeholder={isLiveTraffic ? "Pod Name, or IP" : "Pod Name"} />
                        </CdsInput>
                        <ErrorMessage
                            errors={errors}
                            name="src"
                            as={<ErrorMessageContainer />}
                        />
                        <CdsSelect>
                            <label>Protocol</label>
                            <select {...register("protocol")}>
                                <option value="TCP">TCP</option>
                                <option value="UDP">UDP</option>
                                <option value="ICMP">ICMP</option>
                            </select>
                        </CdsSelect>
                        { (protocol === "TCP" || protocol === "UDP") && <>
                            <CdsInput>
                                <label>Source Port</label>
                                <input type="number" {...srcPort} />
                                { isLiveTraffic && <CdsControlMessage>use 0 to match any port</CdsControlMessage> }
                                { !isLiveTraffic && <CdsControlMessage>use 0 for arbitrary port</CdsControlMessage> }
                            </CdsInput>
                            <ErrorMessage
                                errors={errors}
                                name={srcPort.name}
                                as={<ErrorMessageContainer />}
                            />
                        </> }
                        <CdsRadioGroup>
                            <label>Destination Type</label>
                            <CdsRadio key="pod">
                                <label>Pod</label>
                                <input {...destinationType} type="radio" value="Pod" />
                            </CdsRadio>
                            <CdsRadio key="service">
                                <label>Service</label>
                                <input {...destinationType} type="radio" value="Service" />
                            </CdsRadio>
                            <CdsRadio key="ip">
                                <label>IP</label>
                                <input {...destinationType} type="radio" value="IP" />
                            </CdsRadio>
                        </CdsRadioGroup>
                        <ErrorMessage
                            errors={errors}
                            name="destinationType"
                            as={<ErrorMessageContainer />}
                        />
                        <CdsInput>
                            <label>Destination Namespace</label>
                            <input {...register("dstNamespace")} />
                        </CdsInput>
                        <CdsInput>
                            <label>Destination</label>
                            <input {...register("dst")} placeholder="Pod / Service Name, or IP" />
                        </CdsInput>
                        <ErrorMessage
                            errors={errors}
                            name="dst"
                            as={<ErrorMessageContainer />}
                        />
                        { (protocol === "TCP" || protocol === "UDP") && <>
                            <CdsInput>
                                <label>Destination Port</label>
                                <input type="number" {...dstPort} />
                                { isLiveTraffic && <CdsControlMessage>use 0 to match any port</CdsControlMessage> }
                            </CdsInput>
                            <ErrorMessage
                                errors={errors}
                                name="dstPort"
                                as={<ErrorMessageContainer />}
                            />
                        </> }
                        { (protocol === "TCP" && !isLiveTraffic) && <>
                            <CdsInput>
                                <label>TCP Flags</label>
                                <input type="number" {...tcpFlags} />
                                <CdsControlMessage>use 2 for SYN flag</CdsControlMessage>
                            </CdsInput>
                            <ErrorMessage
                                errors={errors}
                                name="tcpFlags"
                                as={<ErrorMessageContainer />}
                            />
                        </> }
                        <CdsInput>
                            <label>Request Timeout</label>
                            <input type="number" {...timeout} placeholder="Timeout in seconds" />
                        </CdsInput>
                        <div cds-layout="horizontal gap:lg">
                            <CdsCheckbox>
                                <label>Use IPv6</label>
                                <input type="checkbox" {...register("useIPv6")} />
                            </CdsCheckbox>
                            <ErrorMessage
                                errors={errors}
                                name="useIPv6"
                                as={<ErrorMessageContainer />}
                            />
                            <CdsCheckbox>
                                <label>Live Traffic</label>
                                <input type="checkbox" {...register("liveTraffic")} />
                            </CdsCheckbox>
                            { isLiveTraffic &&
                                <CdsCheckbox>
                                    <label>Dropped Traffic Only</label>
                                    <input type="checkbox" {...register("droppedOnly")} />
                                </CdsCheckbox>
                            }
                        </div>
                        <ErrorMessage
                            errors={errors}
                            name="droppedOnly"
                            as={<ErrorMessageContainer />}
                        />
                        <div cds-layout="horizontal gap:lg">
                            <CdsButton role="button" type="submit">Run Traceflow</CdsButton>
                            <CdsButton role="button" type="button" action="outline" onClick={handleReset}>Reset</CdsButton>
                        </div>
                        <TraceflowRunningAlert traceflowRunning={traceflowRunning} />
                    </CdsFormGroup>
                </form>
            </div>
            <div>
                <Outlet />
            </div>
            </div>
        </main>
    );
}
